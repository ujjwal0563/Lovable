package handlers

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type UploadHandler struct {
	db *pgxpool.Pool
}

func NewUploadHandler(db *pgxpool.Pool) *UploadHandler {
	return &UploadHandler{db: db}
}

// Upload handles POST /api/projects/:projectId/upload
// Accepts image files (PNG, JPG, WebP, GIF) up to 5MB
// Stores as base64 in DB — returned to frontend for Claude vision
func (h *UploadHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "projectId")

	// Verify ownership
	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	// Limit request size to 5MB
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)

	// Parse multipart form
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		writeError(w, "file too large — max 5MB", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, "image field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Validate file type
	ext := strings.ToLower(filepath.Ext(header.Filename))
	allowedExts := map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".webp": "image/webp",
		".gif":  "image/gif",
	}
	mimeType, ok := allowedExts[ext]
	if !ok {
		writeError(w, "unsupported file type — use PNG, JPG, WebP or GIF", http.StatusBadRequest)
		return
	}

	// Read file bytes
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	// Detect actual mime type from file header (more reliable than extension)
	detectedMime := http.DetectContentType(data)
	if strings.HasPrefix(detectedMime, "image/") {
		mimeType = detectedMime
	}

	// Encode to base64
	b64 := base64.StdEncoding.EncodeToString(data)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	// Save image record to DB
	var imageID string
	err = h.db.QueryRow(r.Context(),
		`INSERT INTO project_images (project_id, filename, mime_type, data_url, size_bytes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		projectID, header.Filename, mimeType, dataURL, len(data),
	).Scan(&imageID)
	if err != nil {
		writeError(w, "failed to save image", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"id":        imageID,
		"filename":  header.Filename,
		"mime_type": mimeType,
		"size":      len(data),
		"data_url":  dataURL,
	}, http.StatusCreated)
}

// ListImages handles GET /api/projects/:projectId/images
func (h *UploadHandler) ListImages(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "projectId")

	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT id, filename, mime_type, size_bytes, created_at
		 FROM project_images WHERE project_id = $1
		 ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		writeError(w, "failed to load images", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type imageRow struct {
		ID        string `json:"id"`
		Filename  string `json:"filename"`
		MimeType  string `json:"mime_type"`
		SizeBytes int    `json:"size_bytes"`
		CreatedAt string `json:"created_at"`
	}

	images := []imageRow{}
	for rows.Next() {
		var img imageRow
		if err := rows.Scan(&img.ID, &img.Filename, &img.MimeType, &img.SizeBytes, &img.CreatedAt); err != nil {
			continue
		}
		images = append(images, img)
	}

	writeJSON(w, images, http.StatusOK)
}

// DeleteImage handles DELETE /api/projects/:projectId/images/:imageId
func (h *UploadHandler) DeleteImage(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	projectID := chi.URLParam(r, "projectId")
	imageID := chi.URLParam(r, "imageId")

	var ownerID string
	if err := h.db.QueryRow(r.Context(),
		`SELECT user_id FROM projects WHERE id = $1`, projectID,
	).Scan(&ownerID); err != nil || ownerID != userID {
		writeError(w, "project not found", http.StatusNotFound)
		return
	}

	h.db.Exec(r.Context(),
		`DELETE FROM project_images WHERE id = $1 AND project_id = $2`,
		imageID, projectID,
	)

	w.WriteHeader(http.StatusNoContent)
}
