package event

import "time"

// EventType defines what happened
type EventType string

const (
	EventUpload   EventType = "UPLOAD"
	EventDownload EventType = "DOWNLOAD"
	EventDelete   EventType = "DELETE"
	EventNuke     EventType = "NUKE"
	EventRaceEnd  EventType = "RACEEND"
	EventNewUser  EventType = "NEWUSER"
)

// Event represents an FTP event from GoFTPd
type Event struct {
	Type      EventType
	Timestamp time.Time
	User      string
	Group     string
	Section   string
	Filename  string
	Size      int64
	Speed     float64 // MB/s
	Path      string
	Data      map[string]string // For plugins to add custom data
}

// NewEvent creates a new event
func NewEvent(t EventType, user, group, section, filename string) *Event {
	return &Event{
		Type:      t,
		Timestamp: time.Now(),
		User:      user,
		Group:     group,
		Section:   section,
		Filename:  filename,
		Data:      make(map[string]string),
	}
}
