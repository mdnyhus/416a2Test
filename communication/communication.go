/*
	Contains the RPC types used for Server RPC call
*/

package communication

import (
	"fmt"
)

// A Chunk is the unit of reading/writing in DFS.
type Chunk [32]byte
type File [256]*Chunk

// Represents a type of file access.
type FileMode int

const (
	// Read mode.
	READ FileMode = iota

	// Read/Write mode.
	WRITE

	// Disconnected read mode.
	DREAD
)

// ERROR DEFINITIONS
// RPC casts all errors to a ServerError, so interpret any error as a DisconnectedError

// Contains serverAddr
type DisconnectedError string

func (e DisconnectedError) Error() string {
	return fmt.Sprintf("DFS: Not connnected to server [%s]", string(e))
}

type HeartbeatMessage struct {
	Name int
}

type MountClientArgs struct {
	// If client has already been given a name, pass that name here
	// If not, pas "-1"
	Name    int
	Address string
}

type UMountClientArgs struct {
	Name int
}

type OpenArgs struct {
	CName int
	FName string
	Mode  FileMode
}

type OpenReply struct {
	FileData [256]struct {
		New       bool
		Version   int
		ChunkData Chunk
	}
	// RPC errors are all cast to a ServerError
	// Use errType to indicate a specific error; 0 indicates no error
	// 1: OpenWriteConflictError
	// 2: FileUnavailableError
	ErrType int
}

type AckOpenArgs struct {
	CName int
	FName string
	Val   int
}

type FileExistsArgs struct {
	FName string
}

type ReadArgs struct {
	CName    int
	FName    string
	ChunkNum uint8
}

type ReadReply struct {
	ChunkData Chunk
	Version   int
	// RPC errors are all cast to a ServerError
	// Use errType to indicate a specific error; 0 indicates no error
	// 1: ChunkUnavailableError
	// 2: BadFilenameError
	ErrType int
}

type WriteArgs struct {
	CName    int
	FName    string
	ChunkNum uint8
}

type WriteReply struct {
	Version int
	// RPC errors are all cast to a ServerError
	// Use errType to indicate a specific error; 0 indicates no error
	// 1: BadFileModeError
	// 2: WriteModeTimeoutError
	ErrType int
}

type VersionCheckArgs struct {
	CName    int
	FName    string
	ChunkNum uint8
}

type VersionCheckReply struct {
	Version int
	// RPC errors are all cast to a ServerError
	// Use errType to indicate a specific error; 0 indicates no error
	// 1: BadFilenameError
	ErrType int
}

type ChunkData struct {
	ChunkNum uint8
	Version  int
}

type VersionUpdateArgs struct {
	CName    int
	FName    string
	Versions [256]int
}

type VersionUpdateReply struct {
	// RPC errors are all cast to a ServerError
	// Use errType to indicate a specific error; 0 indicates no error
	// 1: BadFilenameError
	ErrType int
}

type CloseArgs struct {
	CName int
	FName string
}

type ReconnectArgs struct {
	CName int
	Files []string
}
