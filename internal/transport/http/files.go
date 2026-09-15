package httptransport

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/go-api-template/internal/apperror"
	"github.com/lihongjie0209/go-api-template/internal/files"
	"github.com/lihongjie0209/go-api-template/internal/pagination"
)

type FileHandler struct {
	service *files.Service
	logger  *slog.Logger
}

func NewFileHandler(service *files.Service, logger *slog.Logger) *FileHandler {
	return &FileHandler{service: service, logger: logger}
}

type FileIDRequest struct {
	ID string `json:"id" binding:"required"`
}
type DeleteFileRequest struct {
	ID      string `json:"id" binding:"required"`
	Version int64  `json:"version" binding:"required,gt=0"`
}
type FilePageRequest struct {
	pagination.Request
	Keyword       string     `json:"keyword"`
	IDs           []string   `json:"ids"`
	ContentTypes  []string   `json:"content_types"`
	CreatedByIDs  []string   `json:"created_by_ids"`
	SizeFrom      *int64     `json:"size_from"`
	SizeTo        *int64     `json:"size_to"`
	CreatedAtFrom *time.Time `json:"created_at_from"`
	CreatedAtTo   *time.Time `json:"created_at_to"`
}

// Upload godoc
// @Summary Upload a file
// @Tags files
// @Accept multipart/form-data
// @Produce json
// @Security Bearer
// @Param file formData file true "File content"
// @Success 200 {object} Response{body=files.Record}
// @Router /api/v1/files/upload [post]
func (h *FileHandler) Upload(c *gin.Context) {
	header, err := c.FormFile("file")
	if err != nil {
		Fail(c, h.logger, apperror.Invalid("file is required", err))
		return
	}
	file, err := header.Open()
	if err != nil {
		Fail(c, h.logger, apperror.Invalid("open uploaded file", err))
		return
	}
	defer func() { _ = file.Close() }()
	sniff := make([]byte, 512)
	read, err := io.ReadFull(file, sniff)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		Fail(c, h.logger, apperror.Invalid("inspect uploaded file", err))
		return
	}
	body := io.MultiReader(bytes.NewReader(sniff[:read]), file)
	record, err := h.service.Upload(c.Request.Context(), files.UploadInput{Name: header.Filename, ContentType: http.DetectContentType(sniff[:read]), Size: header.Size, Body: body})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, record)
}

// FileInfo godoc
// @Summary Get file metadata
// @Tags files
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body FileIDRequest true "File ID"
// @Success 200 {object} Response{body=files.Record}
// @Router /api/v1/files/get [post]
func (h *FileHandler) Get(c *gin.Context) {
	var request FileIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	record, err := h.service.Get(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, record)
}

// Page godoc
// @Summary Page accessible file metadata
// @Tags files
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body FilePageRequest true "File filters"
// @Success 200 {object} Response{body=files.Page}
// @Router /api/v1/files/page [post]
func (h *FileHandler) Page(c *gin.Context) {
	var request FilePageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	page, err := h.service.Page(c.Request.Context(), files.PageInput{Request: request.Request, Keyword: request.Keyword, IDs: request.IDs, ContentTypes: request.ContentTypes, CreatedByIDs: request.CreatedByIDs, SizeFrom: request.SizeFrom, SizeTo: request.SizeTo, CreatedAtFrom: request.CreatedAtFrom, CreatedAtTo: request.CreatedAtTo})
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, page)
}

// Download godoc
// @Summary Create a short-lived file download URL
// @Tags files
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body FileIDRequest true "File ID"
// @Success 200 {object} Response{body=files.Download}
// @Router /api/v1/files/download [post]
func (h *FileHandler) Download(c *gin.Context) {
	var request FileIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	download, err := h.service.Download(c.Request.Context(), request.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	OK(c, download)
}

// DeleteFile godoc
// @Summary Logically delete a file using optimistic locking
// @Tags files
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteFileRequest true "File and version"
// @Success 200 {object} Response
// @Router /api/v1/files/delete [post]
func (h *FileHandler) Delete(c *gin.Context) {
	var request DeleteFileRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid request", err))
		return
	}
	if err := h.service.Delete(c.Request.Context(), request.ID, request.Version); err != nil {
		h.fail(c, err)
		return
	}
	OK(c, gin.H{})
}

func (h *FileHandler) fail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, files.ErrNotFound):
		Fail(c, h.logger, apperror.NotFound("file not found"))
	case errors.Is(err, files.ErrForbidden):
		Fail(c, h.logger, apperror.Forbidden("file access denied"))
	case errors.Is(err, files.ErrVersionConflict):
		Fail(c, h.logger, apperror.Conflict("file version conflict", err))
	case errors.Is(err, files.ErrInvalidInput):
		Fail(c, h.logger, apperror.Invalid("invalid file input", err))
	case errors.Is(err, files.ErrDisabled):
		Fail(c, h.logger, apperror.Unavailable("file service is unavailable", err))
	default:
		Fail(c, h.logger, apperror.Internal(err))
	}
}
