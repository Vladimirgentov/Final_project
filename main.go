package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type PostResponse struct {
	TotalCount      int `json:"total_count"`
	DuplicatesCount int `json:"duplicates_count"`
	TotalItems      int `json:"total_items"`
	TotalCategories int `json:"total_categories"`
	TotalPrice      int `json:"total_price"`
}

type PriceRow struct {
	ProductID  int
	Name       string
	Category   string
	PriceCents int
	CreatedAt  time.Time
}

func connectDB() (*sql.DB, error) {
	host := env("DB_HOST", "localhost")
	port := env("DB_PORT", "5432")
	user := env("DB_USER", "validator")
	pass := env("DB_PASSWORD", "val1dat0r")
	name := env("DB_NAME", "project-sem-1")

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, name,
	)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.Handle("/api/v0/prices", withMethod(
		map[string]http.HandlerFunc{
			http.MethodPost: handlePricesPost(db),
			http.MethodGet:  handlePricesGet(db),
		},
	))

	addr := ":" + env("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	log.Printf("listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

func withMethod(methods map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok := methods[r.Method]
		if !ok {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

func readArchiveBytes(r *http.Request, limit int64) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(limit); err != nil {
			return nil, err
		}
		for _, field := range []string{"file", "archive", "data"} {
			f, _, err := r.FormFile(field)
			if err == nil {
				defer f.Close()
				return io.ReadAll(io.LimitReader(f, limit))
			}
		}
		if r.MultipartForm != nil && len(r.MultipartForm.File) > 0 {
			for _, fhs := range r.MultipartForm.File {
				if len(fhs) == 0 {
					continue
				}
				f, err := fhs[0].Open()
				if err != nil {
					continue
				}
				defer f.Close()
				return io.ReadAll(io.LimitReader(f, limit))
			}
		}
		return nil, errors.New("multipart has no file")
	}
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

func openCSVFromZipBytes(zipBytes []byte) (io.ReadCloser, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, errors.New("invalid zip archive")
	}

	var anyCSV *zip.File

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.Contains(name, "__MACOSX/") || strings.HasSuffix(strings.ToLower(name), ".ds_store") {
			continue
		}
		base := path.Base(name)
		if strings.EqualFold(base, "data.csv") {
			rc, err := f.Open()
			if err != nil {
				return nil, errors.New("failed to open data.csv")
			}
			return rc, nil
		}
		if anyCSV == nil && strings.HasSuffix(strings.ToLower(base), ".csv") {
			anyCSV = f
		}
	}

	if anyCSV != nil {
		rc, err := anyCSV.Open()
		if err != nil {
			return nil, errors.New("failed to open csv")
		}
		return rc, nil
	}

	return nil, errors.New("data.csv not found in archive")
}

func openCSVFromTarBytes(tarBytes []byte) (io.ReadCloser, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))

	var anyCSV []byte

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid tar archive")
		}
		if hdr == nil {
			continue
		}

		name := strings.ReplaceAll(hdr.Name, "\\", "/")
		if strings.Contains(name, "__MACOSX/") || strings.HasSuffix(strings.ToLower(name), ".ds_store") {
			continue
		}

		base := path.Base(name)
		if strings.EqualFold(base, "data.csv") {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, errors.New("failed to read data.csv from tar")
			}
			return io.NopCloser(bytes.NewReader(b)), nil
		}

		if anyCSV == nil && strings.HasSuffix(strings.ToLower(base), ".csv") {
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, errors.New("failed to read csv from tar")
			}
			anyCSV = b
		}
	}

	if anyCSV != nil {
		return io.NopCloser(bytes.NewReader(anyCSV)), nil
	}

	return nil, errors.New("data.csv not found in archive")
}

func handlePricesPost(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		archiveType := strings.TrimSpace(r.URL.Query().Get("type"))
		if archiveType == "" {
			archiveType = "zip"
		}
		if archiveType != "zip" && archiveType != "tar" {
			http.Error(w, "type must be zip or tar", http.StatusBadRequest)
			return
		}

		body, err := readArchiveBytes(r, 50<<20)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		var csvRC io.ReadCloser
		switch archiveType {
		case "zip":
			csvRC, err = openCSVFromZipBytes(body)
		case "tar":
			csvRC, err = openCSVFromTarBytes(body)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer csvRC.Close()

		resp, err := ingestCSV(ctx, db, csvRC)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func ingestCSV(ctx context.Context, db *sql.DB, csvStream io.Reader) (PostResponse, error) {
	br := bufio.NewReader(csvStream)
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.Comma = ','

	_, _ = cr.Read()

	var (
		totalCount      int
		duplicatesCount int
		validRows       []PriceRow
		fileSeen        = make(map[string]struct{})
	)

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return PostResponse{}, errors.New("failed to read csv")
		}

		totalCount++

		row, ok := parseAndValidateCSVRow(rec)
		if !ok {
			duplicatesCount++
			continue
		}

		key := fmt.Sprintf("%s|%s|%d|%s", row.Name, row.Category, row.PriceCents, row.CreatedAt.Format("2006-01-02"))
		if _, exists := fileSeen[key]; exists {
			duplicatesCount++
			continue
		}
		fileSeen[key] = struct{}{}

		validRows = append(validRows, row)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PostResponse{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO prices (product_id, name, category, price, created_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (created_at, name, category, price) DO NOTHING
		RETURNING id
	`)
	if err != nil {
		return PostResponse{}, err
	}
	defer stmt.Close()

	inserted := 0
	for _, row := range validRows {
		var id int
		err := stmt.QueryRowContext(ctx, row.ProductID, row.Name, row.Category, row.PriceCents, row.CreatedAt).Scan(&id)
		if err == sql.ErrNoRows {
			duplicatesCount++
			continue
		}
		if err != nil {
			return PostResponse{}, err
		}
		inserted++
	}

	var totalCategories int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT category) FROM prices`).Scan(&totalCategories); err != nil {
		return PostResponse{}, err
	}

	var totalPriceCents sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(price),0) FROM prices`).Scan(&totalPriceCents); err != nil {
		return PostResponse{}, err
	}
	sumCents := int64(0)
	if totalPriceCents.Valid {
		sumCents = totalPriceCents.Int64
	}

	if err := tx.Commit(); err != nil {
		return PostResponse{}, err
	}

	return PostResponse{
		TotalCount:      totalCount,
		DuplicatesCount: duplicatesCount,
		TotalItems:      inserted,
		TotalCategories: totalCategories,
		TotalPrice:      int(math.Round(float64(sumCents) / 100.0)),
	}, nil
}

func parseAndValidateCSVRow(rec []string) (PriceRow, bool) {
	if len(rec) < 5 {
		return PriceRow{}, false
	}

	idStr := strings.TrimSpace(rec[0])
	name := strings.TrimSpace(rec[1])
	category := strings.TrimSpace(rec[2])
	priceStr := strings.TrimSpace(rec[3])
	dateStr := strings.TrimSpace(rec[4])

	if idStr == "" || name == "" || category == "" || priceStr == "" || dateStr == "" {
		return PriceRow{}, false
	}

	productID, err := strconv.Atoi(idStr)
	if err != nil || productID <= 0 {
		return PriceRow{}, false
	}

	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil || price <= 0 {
		return PriceRow{}, false
	}
	priceCents := int(math.Round(price * 100.0))

	createdAt, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return PriceRow{}, false
	}

	return PriceRow{
		ProductID:  productID,
		Name:       name,
		Category:   category,
		PriceCents: priceCents,
		CreatedAt:  createdAt,
	}, true
}

func handlePricesGet(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		startStr := strings.TrimSpace(r.URL.Query().Get("start"))
		endStr := strings.TrimSpace(r.URL.Query().Get("end"))
		minStr := strings.TrimSpace(r.URL.Query().Get("min"))
		maxStr := strings.TrimSpace(r.URL.Query().Get("max"))

		start, err := time.Parse("2006-01-02", startStr)
		if err != nil {
			http.Error(w, "invalid start", http.StatusBadRequest)
			return
		}
		end, err := time.Parse("2006-01-02", endStr)
		if err != nil {
			http.Error(w, "invalid end", http.StatusBadRequest)
			return
		}
		if end.Before(start) {
			http.Error(w, "end must be >= start", http.StatusBadRequest)
			return
		}

		minI, err := strconv.Atoi(minStr)
		if err != nil || minI <= 0 {
			http.Error(w, "invalid min", http.StatusBadRequest)
			return
		}
		maxI, err := strconv.Atoi(maxStr)
		if err != nil || maxI <= 0 {
			http.Error(w, "invalid max", http.StatusBadRequest)
			return
		}
		if maxI < minI {
			http.Error(w, "max must be >= min", http.StatusBadRequest)
			return
		}

		minCents := minI * 100
		maxCents := maxI * 100

		query := `
			SELECT product_id, name, category, price, created_at
			FROM prices
			WHERE created_at >= $1 AND created_at <= $2 AND price >= $3 AND price <= $4
			ORDER BY created_at ASC, id ASC
		`
		rows, err := db.QueryContext(ctx, query, start, end, minCents, maxCents)
		if err != nil {
			http.Error(w, "db query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var out [][]string
		out = append(out, []string{"id", "name", "category", "price", "create_date"})
		for rows.Next() {
			var (
				productID  int
				name       string
				category   string
				priceCents int
				createdAt  time.Time
			)
			if err := rows.Scan(&productID, &name, &category, &priceCents, &createdAt); err != nil {
				http.Error(w, "db scan failed", http.StatusInternalServerError)
				return
			}
			out = append(out, []string{
				strconv.Itoa(productID),
				name,
				category,
				formatCents(priceCents),
				createdAt.Format("2006-01-02"),
			})
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "db rows failed", http.StatusInternalServerError)
			return
		}

		zipBytes, err := makeZIPWithCSV(out)
		if err != nil {
			http.Error(w, "zip build failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="data.zip"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBytes)
	}
}

func makeZIPWithCSV(rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	f, err := zw.Create("data.csv")
	if err != nil {
		_ = zw.Close()
		return nil, err
	}

	cw := csv.NewWriter(f)
	cw.Comma = ','

	for _, rec := range rows {
		if err := cw.Write(rec); err != nil {
			cw.Flush()
			_ = zw.Close()
			return nil, err
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		_ = zw.Close()
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func formatCents(cents int) string {
	if cents < 0 {
		cents = -cents
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
