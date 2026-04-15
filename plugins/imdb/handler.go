package imdb

import (
	"log"

	"goftpd/internal/user"
)

type Handler struct {
	APIKey string
	Debug  bool
}

// New creates a new imdb handler with defaults.
func New() *Handler {
	return &Handler{
		APIKey: "",
		Debug:  false,
	}
}

func NewHandler(apiKey string, debug bool) *Handler {
	return &Handler{
		APIKey: apiKey,
		Debug:  debug,
	}
}

// Plugin interface implementation

func (h *Handler) Name() string {
	return "imdb"
}

func (h *Handler) Init(config map[string]interface{}) error {
	if apiKey, ok := config["api_key"].(string); ok {
		h.APIKey = apiKey
	}
	if h.Debug {
		log.Printf("[IMDB] Initialized with API key: %s", h.APIKey)
	}
	return nil
}

func (h *Handler) OnUpload(user *user.User, path string, filename string, size int64, speed float64) error {
	// Extract movie/show name from path/filename
	// Query IMDb for ratings, cast, plot, etc.
	// Update release directory with info
	if h.Debug {
		log.Printf("[IMDB] OnUpload: Checking %s for IMDb data (speed=%.1f MB/s)", filename, speed/(1024*1024))
	}
	
	// TODO: Parse filename, query API, add info to release
	return nil
}

func (h *Handler) OnDownload(user *user.User, path string, filename string, size int64) error {
	// Not needed for imdb
	return nil
}

func (h *Handler) OnDirList(user *user.User, path string) (string, error) {
	// Could return IMDb info in directory listing
	return "", nil
}

func (h *Handler) Stop() error {
	if h.Debug {
		log.Printf("[IMDB] Stopping")
	}
	return nil
}
