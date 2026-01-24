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
	TotalCount      int     `json:"total_count"`      // Общее количество строк в файле
	DuplicatesCount int     `json:"duplicates_count"` // Количество дубликатов (дубль = совпадают все поля кроме id) + дубли в БД
	TotalItems      int     `json:"total_items"`      // Количество успешно добавленных элементов в текущей загрузке
	TotalCategories int     `json:"total_categories"` // Общее количество категорий по всей БД
	TotalPrice      float64 `json:"total_price"`      // Суммарная стоимость по всей БД (в основных единицах, напр. 1000.50)
}

// Входной ряд из CSV (id мы читаем, но НЕ вставляем в БД как id)
type PriceRow struct {
	InputID   string
	CreatedAt time.Time
	Name      string
	Category  string
	Price     float64
}

// Ряд из БД для экспорта
type DBRow struct {
	ID        int64
	Name      string
	Category  string
	Price     float64
	CreatedAt time.Time
}

func main() {
	db, err := connectDB()
	if err != nil {
		log.Printf("db connect: %v", err)
		return
	}
	defer func() {
		_ = db.Close()
	}()

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

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		// НЕ log.Fatal, чтобы не обходить defer
		log.Printf("http server error: %v", err)
	}
}

func connectDB() (*sql.DB, error) {
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
		return nil, fmt.Errorf("db open: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close() // важно закрыть коннект при ошибке
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return db, nil
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
	// 1) Сначала читаем CSV целиком и валидируем
	br := bufio.NewReader(csvStream)
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.Comma = ','

	// header
	_, _ = cr.Read()

	var (
		totalCount int

		// Дубликаты во входном файле считаем по всем полям кроме id:
		// created_at | name | category | price
		seenNoID = make(map[string]struct{})

		validRows     []PriceRow
		rejectedAsDup int // сюда же складываем и “плохие строки”, т.к. отдельного поля в ответе нет
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
			rejectedAsDup++
			continue
		}

		inputID := strings.TrimSpace(rec[0])
		name := strings.TrimSpace(rec[1])
		category := strings.TrimSpace(rec[2])
		priceStr := strings.TrimSpace(rec[3])
		createdAtStr := strings.TrimSpace(rec[4])

		if inputID == "" || createdAtStr == "" || name == "" || category == "" || priceStr == "" {
			rejectedAsDup++
			continue
		}

		createdAt, err := time.Parse("2006-01-02", createdAtStr)
		if err != nil {
			rejectedAsDup++
			continue
		}

		price, err := parsePrice(priceStr) // float64
		if err != nil {
			rejectedAsDup++
			continue
		}

		keyNoID := fmt.Sprintf("%s|%s|%s|%.2f", createdAtStr, name, category, price)
		if _, ok := seenNoID[keyNoID]; ok {
			// дубль во входном файле (id игнорируем)
			rejectedAsDup++
			continue
		}
		seenNoID[keyNoID] = struct{}{}

		validRows = append(validRows, PriceRow{
			InputID:   inputID,
			CreatedAt: createdAt,
			Name:      name,
			Category:  category,
			Price:     price,
		})
	}

	// 2) Вся вставка + подсчёт статистики — в одной транзакции
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PostResponse{}, errors.New("db begin failed")
	}
	defer func() { _ = tx.Rollback() }()

	var (
		totalItems      int
		duplicatesCount = rejectedAsDup
	)

	for _, r := range validRows {
		inserted, err := insertPriceTx(ctx, tx, r)
		if err != nil {
			return PostResponse{}, errors.New("db insert failed")
		}
		if !inserted {
			// дубль уже есть в БД (по уникальности “все поля кроме id”)
			duplicatesCount++
			continue
		}
		totalItems++
	}

	totalCategories, totalPrice, err := statsTx(ctx, tx)
	if err != nil {
		return PostResponse{}, errors.New("db stats failed")
	}

	if err := tx.Commit(); err != nil {
		return PostResponse{}, errors.New("db commit failed")
	}

	return PostResponse{
		TotalCount:      totalCount,
		DuplicatesCount: duplicatesCount,
		TotalItems:      totalItems,
		TotalCategories: totalCategories,
		TotalPrice:      totalPrice,
	}, nil
}

func parsePrice(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, errors.New("invalid price")
	}
	// нормализуем до 2 знаков
	f = math.Round(f*100) / 100
	if f <= 0 {
		return 0, errors.New("invalid price")
	}
	return f, nil
}

func insertPriceTx(ctx context.Context, tx *sql.Tx, r PriceRow) (bool, error) {
	// ВАЖНО:
	// - id НЕ вставляем (должен генерироваться)
	// - product_id можно хранить как отдельное поле, но наружу его не отдаём.
	// Уникальность “все поля кроме id” должна быть обеспечена constraint'ом в БД:
	// UNIQUE(created_at, name, category, price)
	const q = `
		INSERT INTO prices (product_id, created_at, name, category, price)
		VALUES ($1, $2::date, $3, $4, $5)
		ON CONFLICT DO NOTHING;
	`
	res, err := tx.ExecContext(ctx, q, r.InputID, r.CreatedAt, r.Name, r.Category, r.Price)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func statsTx(ctx context.Context, tx *sql.Tx) (totalCategories int, totalPrice float64, err error) {
	// Одним запросом
	const q = `
		SELECT
			COUNT(DISTINCT category) AS total_categories,
			COALESCE(SUM(price), 0)  AS total_price
		FROM prices;
	`
	if err := tx.QueryRowContext(ctx, q).Scan(&totalCategories, &totalPrice); err != nil {
		return 0, 0, err
	}
	// нормализуем до 2 знаков (на всякий случай)
	totalPrice = math.Round(totalPrice*100) / 100
	return totalCategories, totalPrice, nil
}

// ------------------------- GET -------------------------

func handlePricesGet(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Параметры могут отсутствовать в любых комбинациях.
		startStr := strings.TrimSpace(r.URL.Query().Get("start"))
		endStr := strings.TrimSpace(r.URL.Query().Get("end"))
		minStr := strings.TrimSpace(r.URL.Query().Get("min"))
		maxStr := strings.TrimSpace(r.URL.Query().Get("max"))

		var (
			startDate time.Time
			endDate   time.Time
			minPrice  float64
			maxPrice  float64

			hasStart bool
			hasEnd   bool
			hasMin   bool
			hasMax   bool
		)

		if startStr != "" {
			d, err := time.Parse("2006-01-02", startStr)
			if err != nil {
				http.Error(w, "invalid start", http.StatusBadRequest)
				return
			}
			startDate = d
			hasStart = true
		}

		if endStr != "" {
			d, err := time.Parse("2006-01-02", endStr)
			if err != nil {
				http.Error(w, "invalid end", http.StatusBadRequest)
				return
			}
			endDate = d
			hasEnd = true
		}

		// min/max по ТЗ — натуральные числа (>0) в основных единицах.
		if minStr != "" {
			i, err := strconv.Atoi(minStr)
			if err != nil || i <= 0 {
				http.Error(w, "invalid min", http.StatusBadRequest)
				return
			}
			minPrice = float64(i)
			hasMin = true
		}

		if maxStr != "" {
			i, err := strconv.Atoi(maxStr)
			if err != nil || i <= 0 {
				http.Error(w, "invalid max", http.StatusBadRequest)
				return
			}
			maxPrice = float64(i)
			hasMax = true
		}

		if hasMin && hasMax && minPrice > maxPrice {
			// можно и просто вернуть пустой набор, но явная ошибка понятнее пользователю
			http.Error(w, "min > max", http.StatusBadRequest)
			return
		}

		query, args := buildGetQuery(hasStart, hasEnd, hasMin, hasMax, startDate, endDate, minPrice, maxPrice)

		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			http.Error(w, "db query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var data []DBRow
		for rows.Next() {
			var rr DBRow
			if err := rows.Scan(&rr.ID, &rr.Name, &rr.Category, &rr.Price, &rr.CreatedAt); err != nil {
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

func buildGetQuery(hasStart, hasEnd, hasMin, hasMax bool, startDate, endDate time.Time, minPrice, maxPrice float64) (string, []any) {
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT id, name, category, price, created_at
		FROM prices
		WHERE 1=1
	`)

	var args []any
	argN := 1

	if hasStart {
		sb.WriteString(fmt.Sprintf(" AND created_at >= $%d", argN))
		args = append(args, startDate)
		argN++
	}

	if hasEnd {
		sb.WriteString(fmt.Sprintf(" AND created_at <= $%d", argN))
		args = append(args, endDate)
		argN++
	}

	if hasMin {
		sb.WriteString(fmt.Sprintf(" AND price >= $%d", argN))
		args = append(args, minPrice)
		argN++
	}

	if hasMax {
		sb.WriteString(fmt.Sprintf(" AND price <= $%d", argN))
		args = append(args, maxPrice)
		argN++
	}

	sb.WriteString(" ORDER BY created_at, id;")
	return sb.String(), args
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
			strconv.FormatInt(r.ID, 10),
			r.Name,
			r.Category,
			formatMoney(r.Price),
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

func formatMoney(v float64) string {
	// строго 2 знака после точки
	return fmt.Sprintf("%.2f", v)
}

// ------------------------- util -------------------------

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
