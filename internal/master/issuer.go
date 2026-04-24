package master

import (
	"encoding/json"
	"fmt"

	"goftpd/internal/protocol"
)

// --------------------------------------------------------------------------
// Issuer functions: Master -> Slave command issuance
//
// Each function fetches an index, sends the command, and returns the index
// so the caller can use rs.FetchResponse(index, timeout) to get the reply.
// --------------------------------------------------------------------------

func IssuePing(rs *RemoteSlave) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "ping"})
}

func IssueDelete(rs *RemoteSlave, path string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "delete", Args: []string{path}})
}

func IssueChmod(rs *RemoteSlave, path string, mode uint32) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "chmod", Args: []string{path, fmt.Sprintf("%o", mode)}})
}

func IssueSymlink(rs *RemoteSlave, linkPath, targetPath string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "symlink", Args: []string{linkPath, targetPath}})
}

func IssueRename(rs *RemoteSlave, from, toDir, toName string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "rename", Args: []string{from, toDir, toName}})
}

func IssueMakeDir(rs *RemoteSlave, path string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "makedir", Args: []string{path}})
}

func IssueChecksum(rs *RemoteSlave, path string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "checksum", Args: []string{path}})
}

func IssueMediaInfo(rs *RemoteSlave, path, binary string, timeoutSeconds int) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "mediainfo",
		Args:  []string{path, binary, fmt.Sprintf("%d", timeoutSeconds)},
	})
}

func IssueTransferStats(rs *RemoteSlave) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "transferStats"})
}

func IssueRunCommand(rs *RemoteSlave, command string, args []string, env map[string]string, timeoutSeconds int, dirPath string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "runCommand",
		Args: []string{
			command,
			fmt.Sprintf("%d", timeoutSeconds),
			string(argsJSON),
			string(envJSON),
			dirPath,
		},
	})
}

// IssueListen tells the slave to open a passive data port.
// ().
func IssueListen(rs *RemoteSlave, encrypted bool, sslClientMode bool) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "listen",
		Args:  []string{fmt.Sprintf("%v:%v", encrypted, sslClientMode)},
	})
}

// IssueConnect tells the slave to connect out to a given address.
// ().
func IssueConnect(rs *RemoteSlave, ip string, port int, encrypted bool, sslClientHandshake bool) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "connect",
		Args:  []string{fmt.Sprintf("%s:%d", ip, port), fmt.Sprintf("%v", encrypted), fmt.Sprintf("%v", sslClientHandshake)},
	})
}

// IssueReceive tells the slave to receive (upload) a file from the FTP client.
// ().
func IssueReceive(rs *RemoteSlave, path string, transferType byte, position int64, inetAddress string, transferIndex int32, minSpeed, maxSpeed int64) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "receive",
		Args: []string{
			string(transferType),
			fmt.Sprintf("%d", position),
			fmt.Sprintf("%d", transferIndex),
			inetAddress,
			path,
			fmt.Sprintf("%d", minSpeed),
			fmt.Sprintf("%d", maxSpeed),
		},
	})
}

// IssueSend tells the slave to send (download) a file to the FTP client.
// ().
func IssueSend(rs *RemoteSlave, path string, transferType byte, position int64, inetAddress string, transferIndex int32, minSpeed, maxSpeed int64) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "send",
		Args: []string{
			string(transferType),
			fmt.Sprintf("%d", position),
			fmt.Sprintf("%d", transferIndex),
			inetAddress,
			path,
			fmt.Sprintf("%d", minSpeed),
			fmt.Sprintf("%d", maxSpeed),
		},
	})
}

// IssueAbort tells the slave to abort a transfer.
func IssueAbort(rs *RemoteSlave, transferIndex int32, reason string) {
	rs.SendCommand(&protocol.AsyncCommand{
		Index: "abort",
		Name:  "abort",
		Args:  []string{fmt.Sprintf("%d", transferIndex), reason},
	})
}

// IssueRemerge tells the slave to scan and send its file listing.
// ().
func IssueRemerge(rs *RemoteSlave, path string, partialRemerge bool, skipAgeCutoff int64, masterTime int64, instantOnline bool) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "remerge",
		Args: []string{
			path,
			fmt.Sprintf("%v", partialRemerge),
			fmt.Sprintf("%d", skipAgeCutoff),
			fmt.Sprintf("%d", masterTime),
			fmt.Sprintf("%v", instantOnline),
		},
	})
}

// IssueCheckSSL checks if slave supports SSL.
func IssueCheckSSL(rs *RemoteSlave) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "checkSSL"})
}

// IssueShutdown tells the slave to shut down.
func IssueShutdown(rs *RemoteSlave) {
	rs.SendCommand(&protocol.AsyncCommand{Index: "shutdown", Name: "shutdown"})
}

// IssueSFVFile asks the slave to parse an SFV file and return the entries.
func IssueSFVFile(rs *RemoteSlave, path string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "sfvFile", Args: []string{path}})
}

// IssueReadFile asks the slave to read a small file and return its content.
// Used for .message, .imdb, .nfo etc.
func IssueReadFile(rs *RemoteSlave, path string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "readFile", Args: []string{path}})
}

// IssueWriteFile asks the slave to write a small file.
func IssueWriteFile(rs *RemoteSlave, path string, content string) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{Index: index, Name: "writeFile", Args: []string{path, content}})
}

func IssueCreateSparseFile(rs *RemoteSlave, path string, size int64) (string, error) {
	index, err := rs.FetchIndex()
	if err != nil {
		return "", err
	}
	return index, rs.SendCommand(&protocol.AsyncCommand{
		Index: index,
		Name:  "createSparseFile",
		Args:  []string{path, fmt.Sprintf("%d", size)},
	})
}
