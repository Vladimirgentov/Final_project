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
	ProductID string
	CreatedAt string
	Name      string
	Category  string
	Price     int
}

type DBRow struct {
	ProductID string
	Name      string
	Category  string
	Price     int
	CreatedAt time.Time
}

func main() {
	db := connectDB()
	defer db.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/api/v0/prices", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handlePricesPost(db)(w, r)
			return
		case http.MethodGet:
			handlePricesGet(db)(w, r)
			return
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	})

	addr := env("HTTP_ADDR", ":8080")
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func connectDB() *sql.DB {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		env("POSTGRES_HOST", "127.0.0.1"),
		env("POSTGRES_PORT", "5432"),
		env("POSTGRES_USER", "validator"),
		env("POSTGRES_PASSWORD", "val1dat0r"),
		env("POSTGRES_DB", "project-sem-1"),
	)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	return db
}

// ------------------------- POST -------------------------

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

		body, err := io.ReadAll(io.LimitReader(r.Body, 50<<20)) // 50MB
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

func openCSVFromZipBytes(zipBytes []byte) (io.ReadCloser, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, errors.New("invalid zip archive")
	}

	for _, f := range zr.File {

		if strings.EqualFold(path.Base(f.Name), "data.csv") {
			rc, err := f.Open()
			if err != nil {
				return nil, errors.New("failed to open data.csv")
			}
			return rc, nil
		}
	}
	return nil, errors.New("data.csv not found in archive")
}

func openCSVFromTarBytes(tarBytes []byte) (io.ReadCloser, error) {
	tr := tar.NewReader(bytes.NewReader(tarBytes))

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

		if strings.EqualFold(path.Base(hdr.Name), "data.csv") {

			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, errors.New("failed to read data.csv from tar")
			}
			return io.NopCloser(bytes.NewReader(b)), nil
		}
	}
	return nil, errors.New("data.csv not found in archive")
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
		totalItems      int
		seen            = make(map[string]struct{})
	)

	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return PostResponse{}, errors.New("invalid csv")
		}

		totalCount++

		if len(rec) != 5 {
			duplicatesCount++
			continue
		}

		productID := strings.TrimSpace(rec[0])
		name := strings.TrimSpace(rec[1])
		category := strings.TrimSpace(rec[2])
		priceStr := strings.TrimSpace(rec[3])
		createdAt := strings.TrimSpace(rec[4])

		if productID == "" || createdAt == "" || name == "" || category == "" || priceStr == "" {
			duplicatesCount++
			continue
		}

		if _, err := time.Parse("2006-01-02", createdAt); err != nil {
			duplicatesCount++
			continue
		}

		price, err := parsePriceToCents(priceStr)
		if err != nil {
			duplicatesCount++
			continue
		}

		key := productID + "|" + createdAt + "|" + name + "|" + category + "|" + strconv.Itoa(price)
		if _, ok := seen[key]; ok {
			duplicatesCount++
			continue
		}
		seen[key] = struct{}{}

		inserted, err := insertPrice(ctx, db, PriceRow{
			ProductID: productID,
			CreatedAt: createdAt,
			Name:      name,
			Category:  category,
			Price:     price,
		})
		if err != nil {
			return PostResponse{}, errors.New("db insert failed")
		}
		if !inserted {
			duplicatesCount++
			continue
		}

		totalItems++
	}

	totalCategories, totalPrice, err := stats(ctx, db)
	if err != nil {
		return PostResponse{}, errors.New("db stats failed")
	}

	return PostResponse{
		TotalCount:      totalCount,
		DuplicatesCount: duplicatesCount,
		TotalItems:      totalItems,
		TotalCategories: totalCategories,
		TotalPrice:      totalPrice,
	}, nil
}

func parsePriceToCents(s string) (int, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, errors.New("invalid price")
	}
	cents := int(math.Round(f * 100))
	if cents <= 0 {
		return 0, errors.New("invalid price")
	}
	return cents, nil
}

func insertPrice(ctx context.Context, db *sql.DB, r PriceRow) (inserted bool, err error) {
	const q = `
		INSERT INTO prices (product_id, created_at, name, category, price)
		VALUES ($1, $2::date, $3, $4, $5)
		ON CONFLICT DO NOTHING;
	`
	res, err := db.ExecContext(ctx, q, r.ProductID, r.CreatedAt, r.Name, r.Category, r.Price)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func stats(ctx context.Context, db *sql.DB) (totalCategories int, totalPrice int, err error) {
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT category) FROM prices;`).Scan(&totalCategories); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(price),0) FROM prices;`).Scan(&totalPrice); err != nil {
		return 0, 0, err
	}
	return totalCategories, totalPrice, nil
}

// ------------------------- GET -------------------------

func handlePricesGet(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		startStr := strings.TrimSpace(r.URL.Query().Get("start"))
		endStr := strings.TrimSpace(r.URL.Query().Get("end"))
		minStr := strings.TrimSpace(r.URL.Query().Get("min"))
		maxStr := strings.TrimSpace(r.URL.Query().Get("max"))

		if startStr == "" || endStr == "" || minStr == "" || maxStr == "" {
			http.Error(w, "missing query params", http.StatusBadRequest)
			return
		}

		startDate, err := time.Parse("2006-01-02", startStr)
		if err != nil {
			http.Error(w, "invalid start", http.StatusBadRequest)
			return
		}
		endDate, err := time.Parse("2006-01-02", endStr)
		if err != nil {
			http.Error(w, "invalid end", http.StatusBadRequest)
			return
		}

		minInt, err := strconv.Atoi(minStr)
		if err != nil || minInt <= 0 {
			http.Error(w, "invalid min", http.StatusBadRequest)
			return
		}
		maxInt, err := strconv.Atoi(maxStr)
		if err != nil || maxInt <= 0 {
			http.Error(w, "invalid max", http.StatusBadRequest)
			return
		}
		if minInt > maxInt {
			http.Error(w, "min > max", http.StatusBadRequest)
			return
		}

		minCents := minInt * 100
		maxCents := maxInt * 100

		rows, err := db.QueryContext(ctx, `
			SELECT product_id, name, category, price, created_at
			FROM prices
			WHERE created_at >= $1 AND created_at <= $2
			  AND price >= $3 AND price <= $4
			ORDER BY created_at, product_id;
		`, startDate, endDate, minCents, maxCents)
		if err != nil {
			http.Error(w, "db query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var data []DBRow
		for rows.Next() {
			var rr DBRow
			if err := rows.Scan(&rr.ProductID, &rr.Name, &rr.Category, &rr.Price, &rr.CreatedAt); err != nil {
				http.Error(w, "db scan failed", http.StatusInternalServerError)
				return
			}
			data = append(data, rr)
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "db rows failed", http.StatusInternalServerError)
			return
		}

		zipBytes, err := buildZipCSV(data)
		if err != nil {
			http.Error(w, "failed to build zip", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="data.zip"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBytes)
	}
}

func buildZipCSV(rows []DBRow) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	fw, err := zw.Create("data.csv")
	if err != nil {
		_ = zw.Close()
		return nil, err
	}

	cw := csv.NewWriter(fw)
	cw.Comma = ','

	if err := cw.Write([]string{"id", "name", "category", "price", "create_date"}); err != nil {
		cw.Flush()
		_ = zw.Close()
		return nil, err
	}

	for _, r := range rows {
		rec := []string{
			r.ProductID,
			r.Name,
			r.Category,
			formatCents(r.Price),
			r.CreatedAt.Format("2006-01-02"),
		}
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

// ------------------------- util -------------------------

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
