package event

import "time"

type EventType string

const (
	EventUpload           EventType = "UPLOAD"
	EventDownload         EventType = "DOWNLOAD"
	EventDelete           EventType = "DELETE"
	EventNuke             EventType = "NUKE"
	EventRaceEnd          EventType = "RACEEND"
	EventRaceStats        EventType = "RACESTATS"
	EventRaceUser         EventType = "RACEUSER"
	EventRaceFooter       EventType = "RACEFOOTER"
	EventNewUser          EventType = "NEWUSER"
	EventLoginFail        EventType = "LOGINFAIL"
	EventSelfIP           EventType = "SELFIP"
	EventSlaveAuthFail    EventType = "SLAVEAUTHFAIL"
	EventSlowUploadWarn   EventType = "SLOWUPLOADWARN"
	EventSlowUploadKick   EventType = "SLOWUPLOADKICK"
	EventSlowDownloadWarn EventType = "SLOWDOWNLOADWARN"
	EventSlowDownloadKick EventType = "SLOWDOWNLOADKICK"
	EventMKDir            EventType = "MKDIR"
	EventRMDir            EventType = "RMDIR"
	EventRename           EventType = "RENAME"
	EventUnnuke           EventType = "UNNUKE"
	EventInvite           EventType = "INVITE"
	EventCommand          EventType = "COMMAND"
	EventDiskStatus       EventType = "DISKSTATUS"
	EventNewDay           EventType = "NEWDAY"
	EventAudioInfo        EventType = "AUDIOINFO"
	EventMediaInfo        EventType = "MEDIAINFO"
	EventSpeedtest        EventType = "SPEEDTEST"
	EventPre              EventType = "PRE"
	EventPreAudioInfo     EventType = "PREAUDIOINFO"
	EventPreMovieInfo     EventType = "PREMOVIEINFO"
	EventPreTVInfo        EventType = "PRETVINFO"
	EventPreBW            EventType = "PREBW"
	EventPreBWUser        EventType = "PREBWUSER"
	EventPreBWInterval    EventType = "PREBWINTERVAL"
	EventNewPreTime       EventType = "NEWPRETIME"
	EventOldPreTime       EventType = "OLDPRETIME"
)

type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	User      string            `json:"user,omitempty"`
	Group     string            `json:"group,omitempty"`
	Section   string            `json:"section,omitempty"`
	Filename  string            `json:"filename,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Speed     float64           `json:"speed,omitempty"`
	Path      string            `json:"path,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
}

func NewEvent(t EventType, user, group, section, filename string) *Event {
	return &Event{Type: t, Timestamp: time.Now(), User: user, Group: group, Section: section, Filename: filename, Data: make(map[string]string)}
}
