package protocol

import (
	"encoding/gob"
	"fmt"
	"io"
	"sync"
)

func init() {
	gob.Register(&AsyncCommand{})
	gob.Register(&AsyncResponse{})
	gob.Register(&AsyncResponseError{})
	gob.Register(&AsyncResponseTransfer{})
	gob.Register(&AsyncResponseTransferStatus{})
	gob.Register(&AsyncResponseDiskStatus{})
	gob.Register(&AsyncResponseRemerge{})
	gob.Register(&AsyncResponseChecksum{})
	gob.Register(&AsyncResponseSSLCheck{})
	gob.Register(&AsyncResponseMaxPath{})
	gob.Register(&AsyncResponseSiteBotMessage{})
	gob.Register(&AsyncResponseSFVInfo{})
	gob.Register(&AsyncResponseFileContent{})
	gob.Register(&AsyncResponseZipEntryContent{})
	gob.Register(&AsyncResponseMediaInfo{})
	gob.Register(&AsyncResponseTransferStats{})
	gob.Register(&AsyncResponseCommandResult{})
	gob.Register(&ConnectInfo{})
	gob.Register(&TransferStatus{})
	gob.Register(&TransferLiveStat{})
	gob.Register(&DiskStatus{})
	gob.Register(&LightRemoteInode{})
	gob.Register([]LightRemoteInode{})
	gob.Register([]TransferLiveStat{})
	gob.Register(&SFVEntry{})
	gob.Register([]SFVEntry{})
	gob.Register(map[string]string{})
}

// --------------------------------------------------------------------------
// Commands: Master -> Slave
// --------------------------------------------------------------------------

type AsyncCommand struct {
	Index string
	Name  string
	Args  []string
}

func (ac *AsyncCommand) String() string {
	return fmt.Sprintf("AsyncCommand[index=%s, name=%s, args=%v]", ac.Index, ac.Name, ac.Args)
}

// --------------------------------------------------------------------------
// Responses: Slave -> Master (each has GetIndex() for routing)
// --------------------------------------------------------------------------

type Indexable interface {
	GetIndex() string
}

type AsyncResponse struct {
	Index string
}

func (ar *AsyncResponse) GetIndex() string { return ar.Index }

type AsyncResponseError struct {
	Index   string
	Message string
}

func (ar *AsyncResponseError) GetIndex() string { return ar.Index }

type AsyncResponseTransfer struct {
	Index string
	Info  ConnectInfo
}

func (ar *AsyncResponseTransfer) GetIndex() string { return ar.Index }

type ConnectInfo struct {
	Port          int
	TransferIndex int32
}

type AsyncResponseTransferStatus struct {
	Status TransferStatus
}

func (ar *AsyncResponseTransferStatus) GetIndex() string { return "TransferStatus" }

type TransferStatus struct {
	TransferIndex int32
	Elapsed       int64
	Transferred   int64
	Checksum      uint32
	Finished      bool
	Error         string
}

type AsyncResponseDiskStatus struct {
	Status DiskStatus
}

func (ar *AsyncResponseDiskStatus) GetIndex() string { return "DiskStatus" }

type DiskStatus struct {
	SpaceAvailable int64
	SpaceCapacity  int64
}

type AsyncResponseRemerge struct {
	Path         string
	Files        []LightRemoteInode
	LastModified int64
}

func (ar *AsyncResponseRemerge) GetIndex() string { return "Remerge" }

type LightRemoteInode struct {
	Name         string
	IsDir        bool
	IsSymlink    bool
	LinkTarget   string
	Size         int64
	LastModified int64
	Owner        string
	Group        string
}

type AsyncResponseChecksum struct {
	Index    string
	Checksum uint32
}

func (ar *AsyncResponseChecksum) GetIndex() string { return ar.Index }

type AsyncResponseSSLCheck struct {
	Index    string
	SSLReady bool
}

func (ar *AsyncResponseSSLCheck) GetIndex() string { return ar.Index }

type AsyncResponseMaxPath struct {
	Index   string
	MaxPath int
}

func (ar *AsyncResponseMaxPath) GetIndex() string { return ar.Index }

type AsyncResponseSiteBotMessage struct {
	Message string
}

func (ar *AsyncResponseSiteBotMessage) GetIndex() string { return "SiteBotMessage" }

// --------------------------------------------------------------------------
// ObjectStream: thread-safe gob encoder/decoder over a connection
// --------------------------------------------------------------------------

type ObjectStream struct {
	enc   *gob.Encoder
	dec   *gob.Decoder
	encMu sync.Mutex
	conn  io.ReadWriteCloser
}

func NewObjectStream(conn io.ReadWriteCloser) *ObjectStream {
	return &ObjectStream{
		enc:  gob.NewEncoder(conn),
		dec:  gob.NewDecoder(conn),
		conn: conn,
	}
}

func (os *ObjectStream) WriteObject(obj interface{}) error {
	os.encMu.Lock()
	defer os.encMu.Unlock()
	return os.enc.Encode(&obj)
}

func (os *ObjectStream) ReadObject() (interface{}, error) {
	var obj interface{}
	err := os.dec.Decode(&obj)
	return obj, err
}

func (os *ObjectStream) Close() error {
	return os.conn.Close()
}

// --------------------------------------------------------------------------
// Zipscript protocol types (slave parses SFV, sends to master)
// --------------------------------------------------------------------------

// SFVEntry is a single file→CRC32 entry from an SFV file.
type SFVEntry struct {
	FileName string
	CRC32    uint32
}

// AsyncResponseSFVInfo is returned when slave parses an SFV file.
type AsyncResponseSFVInfo struct {
	Index    string
	SFVName  string     // name of the .sfv file
	Entries  []SFVEntry // filename→CRC32 entries
	Checksum uint32     // CRC32 of the SFV file itself
}

func (ar *AsyncResponseSFVInfo) GetIndex() string { return ar.Index }

// AsyncResponseFileContent returns small file content from slave.
// Used for .message, .imdb, .nfo display without setting up a full transfer.
type AsyncResponseFileContent struct {
	Index   string
	Content []byte
}

func (ar *AsyncResponseFileContent) GetIndex() string { return ar.Index }

// AsyncResponseZipEntryContent returns a small file embedded inside a zip archive.
type AsyncResponseZipEntryContent struct {
	Index     string
	EntryName string
	Content   []byte
}

func (ar *AsyncResponseZipEntryContent) GetIndex() string { return ar.Index }

// AsyncResponseMediaInfo returns flattened mediainfo metadata from a slave.
type AsyncResponseMediaInfo struct {
	Index  string
	Fields map[string]string
}

func (ar *AsyncResponseMediaInfo) GetIndex() string { return ar.Index }

type AsyncResponseTransferStats struct {
	Index string
	Stats []TransferLiveStat
}

func (ar *AsyncResponseTransferStats) GetIndex() string { return ar.Index }

type AsyncResponseCommandResult struct {
	Index  string
	Output string
}

func (ar *AsyncResponseCommandResult) GetIndex() string { return ar.Index }

type TransferLiveStat struct {
	TransferIndex int32
	Direction     byte
	Path          string
	StartedUnixMs int64
	Transferred   int64
	SpeedBytes    int64
}
