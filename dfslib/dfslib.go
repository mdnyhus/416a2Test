/*

This package specifies the application's interface to the distributed
file system (DFS) system to be used in assignment 2 of UBC CS 416
2017W2.

*/

package dfslib

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"../communication"
)

// A Chunk is the unit of reading/writing in DFS.
type Chunk [32]byte

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

// ////////////////////////////////////////////////////////////////////////////////////////////
// <ERROR DEFINITIONS>

// These type definitions allow the application to explicitly check
// for the kind of error that occurred. Each API call below lists the
// errors that it is allowed to raise.
//
// Also see:
// https://blog.golang.org/error-handling-and-go
// https://blog.golang.org/errors-are-values

// Contains serverAddr
type DisconnectedError string

func (e DisconnectedError) Error() string {
	return fmt.Sprintf("DFS: Not connnected to server [%s]", string(e))
}

// Contains chunkNum that is unavailable
type ChunkUnavailableError uint8

func (e ChunkUnavailableError) Error() string {
	return fmt.Sprintf("DFS: Latest verson of chunk [%s] unavailable", string(e))
}

// Contains filename
type OpenWriteConflictError string

func (e OpenWriteConflictError) Error() string {
	return fmt.Sprintf("DFS: Filename [%s] is opened for writing by another client", string(e))
}

// Contains file mode that is bad.
type BadFileModeError FileMode

func (e BadFileModeError) Error() string {
	return fmt.Sprintf("DFS: Cannot perform this operation in current file mode [%s]", string(e))
}

// Contains filename.
type WriteModeTimeoutError string

func (e WriteModeTimeoutError) Error() string {
	return fmt.Sprintf("DFS: Write access to filename [%s] has timed out; reopen the file", string(e))
}

// Contains filename
type BadFilenameError string

func (e BadFilenameError) Error() string {
	return fmt.Sprintf("DFS: Filename [%s] includes illegal characters or has the wrong length", string(e))
}

// Contains filename
type FileUnavailableError string

func (e FileUnavailableError) Error() string {
	return fmt.Sprintf("DFS: Filename [%s] is unavailable", string(e))
}

// Contains local path
type LocalPathError string

func (e LocalPathError) Error() string {
	return fmt.Sprintf("DFS: Cannot access local path [%s]", string(e))
}

// Contains filename
type FileDoesNotExistError string

func (e FileDoesNotExistError) Error() string {
	return fmt.Sprintf("DFS: Cannot open file [%s] in D mode as it does not exist locally", string(e))
}

// </ERROR DEFINITIONS>
////////////////////////////////////////////////////////////////////////////////////////////

// Represents a file in the DFS system.
type DFSFile interface {
	// Reads chunk number chunkNum into storage pointed to by
	// chunk. Returns a non-nil error if the read was unsuccessful.
	//
	// Can return the following errors:
	// - DisconnectedError (in READ,WRITE modes)
	// - ChunkUnavailableError (in READ,WRITE modes)
	Read(chunkNum uint8, chunk *Chunk) (err error)

	// Writes chunk number chunkNum from storage pointed to by
	// chunk. Returns a non-nil error if the write was unsuccessful.
	//
	// Can return the following errors:
	// - BadFileModeError (in READ,DREAD modes)
	// - DisconnectedError (in WRITE mode)
	// - WriteModeTimeoutError (in WRITE mode)
	Write(chunkNum uint8, chunk *Chunk) (err error)

	// Closes the file/cleans up. Can return the following errors:
	// - DisconnectedError
	Close() (err error)
}

// Represents a connection to the DFS system.
type DFS interface {
	// Check if a file with filename fname exists locally (i.e.,
	// available for DREAD reads).
	//
	// Can return the following errors:
	// - BadFilenameError (if filename contains non alpha-numeric chars or is not 1-16 chars long)
	LocalFileExists(fname string) (exists bool, err error)

	// Check if a file with filename fname exists globally.
	//
	// Can return the following errors:
	// - BadFilenameError (if filename contains non alpha-numeric chars or is not 1-16 chars long)
	// - DisconnectedError
	GlobalFileExists(fname string) (exists bool, err error)

	// Opens a filename with name fname using mode. Creates the file
	// in READ/WRITE modes if it does not exist. Returns a handle to
	// the file through which other operations on this file can be
	// made.
	//
	// Can return the following errors:
	// - OpenWriteConflictError (in WRITE mode)
	// - DisconnectedError (in READ,WRITE modes)
	// - FileUnavailableError (in READ,WRITE modes)
	// - FileDoesNotExistError (in DREAD mode)
	// - BadFilenameError (if filename contains non alpha-numeric chars or is not 1-16 chars long)
	Open(fname string, mode FileMode) (f DFSFile, err error)

	// Disconnects from the server. Can return the following errors:
	// - DisconnectedError
	UMountDFS() (err error)
}

/////////////////////////////////////////////////////////////////////
// TYPE IMPLEMENTATIONS

// Allows server to send file information to client
type ClientRPC struct {
	dfs *DFST
}

func (clientRPC *ClientRPC) ReadRPC(args *communication.ReadArgs, reply *communication.ReadReply) (err error) {
	// check if file exists
	if exists, _ := clientRPC.dfs.LocalFileExists(args.FName); !exists {
		// BadFilenameError
		reply.ErrType = 2
		return nil
	}

	// file should exist locally; open it
	f, err := os.Open(filepath.Join(clientRPC.dfs.localPath, args.FName+".dfs"))
	if err != nil {
		// BadFilenameError
		reply.ErrType = 2
		return nil
	}

	_, err = f.ReadAt(reply.ChunkData[:], int64(len(reply.ChunkData)*int(args.ChunkNum)))
	if err != nil {
		// Shouldn't have an EOF or any other error
		// BadFilenameError
		reply.ErrType = 2
		return nil
	}

	err = f.Close()
	if err != nil {
		// Shouldn't have an EOF or any other error
		// BadFilenameError
		reply.ErrType = 2
		return nil
	}

	return nil
}

// DFSFileType implements the DFSFile above according to its specs
type DFSFileT struct {
	file         *os.File
	fname        string
	fileMode     FileMode
	dfs          *DFST
	disconnected bool
	closed       bool
	versions     [256]int
}

type dFSFileVersions struct {
	Versions [256]int
}

func (dfsFile *DFSFileT) Read(chunkNum uint8, chunk *Chunk) (err error) {
	if dfsFile.disconnected || dfsFile.closed {
		// once an operation returned a DisconnectedError, all future operations should fail
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	var e error

	if !dfsFile.dfs.connected {
		if e = dfsFile.dfs.reconnect(); e != nil && dfsFile.fileMode != DREAD {
			return e
		}
	}

	// create meta with original data
	meta := metadata{
		Name:  dfsFile.dfs.name,
		State: 2,
		MetaRW: metadataRW{
			FName:    dfsFile.fname,
			ChunkNum: chunkNum}}
	// read in original chunk
	dfsFile.file.ReadAt(meta.MetaRW.ChunkData[:], int64(len(*chunk)*int(chunkNum)))
	// log read to allow system to recover
	dfsFile.dfs.writeMetadata(meta)

	args := &communication.ReadArgs{CName: dfsFile.dfs.name, FName: dfsFile.fname, ChunkNum: chunkNum}
	var reply communication.ReadReply
	if dfsFile.dfs.client != nil {
		e = dfsFile.dfs.client.Call("DFSServer.Read", args, &reply)
	}

	if dfsFile.fileMode == DREAD && (e != nil || reply.ErrType != 0) {
		if e != nil {
			dfsFile.dfs.connected = false
		}
		// if there is an error but file is in DREAD mode, get data locally
		dfsFile.file.ReadAt(chunk[:], int64(len(*chunk)*int(chunkNum)))
		// delete log
		dfsFile.dfs.resetMetadata()
		return nil
	} else if reply.ErrType == 1 {
		// delete log
		dfsFile.dfs.resetMetadata()
		// there was a ChunkUnavailableError
		return ChunkUnavailableError(chunkNum)
	} else if e != nil || reply.ErrType != 0 {
		// delete log
		dfsFile.dfs.resetMetadata()
		// there was some other type of error
		dfsFile.disconnected = true
		dfsFile.dfs.connected = false
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	// panic("hello")

	// need to write chunk to memory
	dfsFile.file.WriteAt(reply.ChunkData[:], int64(len(reply.ChunkData)*int(args.ChunkNum)))
	dfsFile.file.Sync()

	dfsFile.versions[chunkNum] = reply.Version
	dfsFile.dfs.updateVersions(dfsFile.fname, dfsFile.versions)

	// need to return read chunk
	copy(chunk[:], reply.ChunkData[:])

	// delete log
	dfsFile.dfs.resetMetadata()

	return nil
}

func (dfsFile *DFSFileT) Write(chunkNum uint8, chunk *Chunk) (err error) {
	if dfsFile.disconnected || dfsFile.closed {
		// once an operation returned a DisconnectedError, all future operations should fail
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	if dfsFile.fileMode == READ || dfsFile.fileMode == DREAD {
		return BadFileModeError(communication.FileMode(dfsFile.fileMode))
	}

	var e error

	if !dfsFile.dfs.connected {
		if e = dfsFile.dfs.reconnect(); e != nil {
			return e
		}
	}

	// log write to allow system to recover
	dfsFile.dfs.writeMetadata(metadata{
		Name:  dfsFile.dfs.name,
		State: 1,
		MetaRW: metadataRW{
			FName:     dfsFile.fname,
			ChunkNum:  chunkNum,
			ChunkData: *chunk}})

	// For now return DisconnectedError
	args := &communication.WriteArgs{CName: dfsFile.dfs.name, FName: dfsFile.fname, ChunkNum: chunkNum}
	var reply communication.WriteReply
	e = dfsFile.dfs.client.Call("DFSServer.Write", args, &reply)

	if reply.ErrType == 2 {
		// delete log
		dfsFile.dfs.resetMetadata()
		return WriteModeTimeoutError(dfsFile.fname)
	} else if e != nil || reply.ErrType != 0 {
		// there was an error
		// Note: if reply.ErrType == 1, server claims there was a BadFileModeError
		// But already checked for that, so just return a DisconnectedError
		// delete log
		dfsFile.dfs.resetMetadata()
		dfsFile.disconnected = true
		dfsFile.dfs.connected = false
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	dfsFile.file.WriteAt(chunk[:], int64(len(*chunk)*int(args.ChunkNum)))
	dfsFile.file.Sync()

	dfsFile.versions[chunkNum] = reply.Version
	dfsFile.dfs.updateVersions(dfsFile.fname, dfsFile.versions)

	// delete log
	dfsFile.dfs.resetMetadata()

	return nil
}

func (dfsFile *DFSFileT) Close() (err error) {
	if dfsFile.closed {
		// file was already closed
		return nil
	}

	if !dfsFile.dfs.connected {
		_ = dfsFile.dfs.reconnect()
	}

	// should still close fd even if disconnected
	dfsFile.file.Close()
	// remove from map
	delete(dfsFile.dfs.files, dfsFile.fname)

	// update closed bool
	dfsFile.closed = true

	args := &communication.CloseArgs{CName: dfsFile.dfs.name, FName: dfsFile.fname}
	var reply int
	if dfsFile.dfs.client != nil {
		if e := dfsFile.dfs.client.Call("DFSServer.Close", args, &reply); e != nil {
			dfsFile.disconnected = true
			dfsFile.dfs.connected = false
			return DisconnectedError(dfsFile.dfs.serverAddr)
		}
	} else {
		dfsFile.disconnected = true
		dfsFile.dfs.connected = false
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	if dfsFile.disconnected {
		// once an operation returned a DisconnectedError, all future operations should fail
		return DisconnectedError(dfsFile.dfs.serverAddr)
	}

	return nil
}

// contains metadata that needs to be saved to disk for this session
type metadataRW struct {
	FName     string
	ChunkNum  uint8
	ChunkData Chunk
}

// need to save entire file for rollback
type metadataOpen struct {
	FName    string
	New      bool
	Versions [256]int
	File     [256]Chunk
}

type metadata struct {
	Name int
	// state indicates what state the system crashed in
	// 0 - no crashes
	// 1 - during write (check metadataRW)
	// 2 - during read (check metadataRW)
	// 3 - during open
	State    int
	MetaRW   metadataRW
	MetaOpen metadataOpen
}

// DFSType implements the DFS above according to its specs
type DFST struct {
	serverAddr string
	localAddr  string
	localPath  string
	// probably a better way of doing this...
	connected         bool
	localTCPAddr      *net.TCPAddr
	serverTCPAddr     *net.TCPAddr
	client            *rpc.Client
	stopHeartbeatChan chan bool
	name              int

	// files []*DFSFileT
	files map[string]*DFSFileT
	// make(map[string]*GlobalFile)
}

func (dfs *DFST) getFullFilePath(fname string) string {
	return filepath.Join(dfs.localPath, fname+".dfs")
}

// Helper function to check whether a filename is valid
// File name is invalid if it contains non alpha-numeric chars or is not 1-16
// chars long, or uses upper case letters
func checkFileName(fname string) (valid bool) {
	if len(fname) <= 0 || len(fname) > 16 {
		// length tests failed
		return false
	} else if !regexp.MustCompile(`^[a-z0-9]+$`).MatchString(fname) {
		// lower-case alpha-numeric tests failed
		return false
	}
	return true
}

func (dfs *DFST) LocalFileExists(fname string) (exists bool, err error) {
	// return BadFilenameError if checkFileName fails
	if !checkFileName(fname) {
		return false, BadFilenameError(fname)
	}

	// otherwise, check the status of the file
	_, e := os.Stat(dfs.getFullFilePath(fname))
	return (e == nil), nil
}

func (dfs *DFST) GlobalFileExists(fname string) (exists bool, err error) {
	// return BadFilenameError if checkFileName fails
	if !checkFileName(fname) {
		return false, BadFilenameError(fname)
	}

	// otherwise, check the status of the file globally
	args := &communication.FileExistsArgs{FName: fname}
	if e := dfs.client.Call("DFSServer.FileExists", args, &exists); e != nil {
		return false, DisconnectedError(dfs.serverAddr)
	}

	return exists, nil
}

func (dfs *DFST) Open(fname string, mode FileMode) (f DFSFile, err error) {
	// return BadFilenameError if checkFileName fails
	if !checkFileName(fname) {
		return nil, BadFilenameError(fname)
	}

	var e error

	if !dfs.connected {
		if e = dfs.reconnect(); e != nil && mode != DREAD {
			return nil, e
		}
	}

	if _, ok := dfs.files[fname]; !ok {
		dfs.files[fname] = &DFSFileT{
			fname:        fname,
			fileMode:     mode,
			dfs:          dfs,
			disconnected: false,
			closed:       false}
	}

	args := &communication.OpenArgs{
		CName: dfs.name,
		FName: fname,
		Mode:  communication.FileMode(mode)}
	var reply communication.OpenReply
	// if this is false, e == nil from above
	if dfs.client != nil {
		e = dfs.client.Call("DFSServer.Open", args, &reply)
	}
	if mode == DREAD && (e != nil || reply.ErrType != 0) {
		if e != nil {
			dfs.connected = false
		}
		// check if the file exists locally; already checked for bad fname
		if exists, _ := dfs.LocalFileExists(fname); !exists {
			return nil, FileDoesNotExistError(fname)
		}
		// if there is an error but opening in DREAD mode, open file anyway
		file, _ := os.Open(dfs.getFullFilePath(fname))
		return &DFSFileT{file: file, fname: fname, fileMode: mode, dfs: dfs}, nil
	} else if reply.ErrType == 1 && mode == WRITE {
		// there was a OpenWriteConflictError
		return nil, OpenWriteConflictError(fname)
	} else if reply.ErrType == 2 {
		// there was a FileUnavailableError
		return nil, FileUnavailableError(fname)
	} else if e != nil || reply.ErrType != 0 {
		// there was some other type of error
		return nil, DisconnectedError(dfs.serverAddr)
	}

	exists, e := dfs.LocalFileExists(fname)
	if e != nil {
		return nil, BadFilenameError(fname)
	}

	ackArgs := &communication.AckOpenArgs{CName: dfs.name, FName: fname, Val: -1}
	var ackReply int

	// open the file, creating it if it hadn't already been created
	file, e := os.OpenFile(dfs.getFullFilePath(fname), os.O_RDWR|os.O_CREATE, 0666)
	if e != nil {
		// send an ack with an error
		if e = dfs.client.Call("DFSServer.AckOpen", ackArgs, &ackReply); e != nil {
			return nil, DisconnectedError(dfs.serverAddr)
		}
		return nil, DisconnectedError(dfs.serverAddr)
	}

	// Now that the file is opened, if any operation fails, we need to abandon the write
	// entirely. Do this by:
	// - closing the file
	// - deleting the file
	// - sending a negative ack to the server
	// - returning an error

	// prepare metadata in case of crash
	meta := metadata{
		Name:  dfs.name,
		State: 3,
		MetaOpen: metadataOpen{
			FName: fname,
			New:   !exists}}

	if !exists {
		// if the file didn't already exist, truncate it to the correct size
		// todo - way to get length of types?
		if e = file.Truncate(int64(32 * 256)); e != nil {
			// abandon write
			file.Close()
			os.Remove(dfs.getFullFilePath(fname))

			// send an ack with an error
			if e = dfs.client.Call("DFSServer.AckOpen", ackArgs, &ackReply); e != nil {
				return nil, DisconnectedError(dfs.serverAddr)
			}

			return nil, DisconnectedError(dfs.serverAddr)
		}
	} else {
		// file arleady exists; write original contents into meta object
		meta.MetaOpen.Versions = dfs.readVersions(fname)
		for i := 0; i < 256; i++ {
			file.ReadAt((meta.MetaOpen.File[i])[:], int64(len(meta.MetaOpen.File[i])*i))
		}
	}

	var versions [256]int
	for i := 0; i < len(reply.FileData); i++ {
		versions[i] = reply.FileData[i].Version
		if reply.FileData[i].New {
			chunk := reply.FileData[i].ChunkData
			_, e = file.WriteAt(chunk[:], int64(len(chunk)*i))
			if e != nil {
				// abandon write
				file.Close()
				os.Remove(dfs.getFullFilePath(fname))

				// send an ack with an error
				if e = dfs.client.Call("DFSServer.AckOpen", ackArgs, &ackReply); e != nil {
					return nil, DisconnectedError(dfs.serverAddr)
				}

				return nil, DisconnectedError(dfs.serverAddr)
			}
		}
	}

	// log open to allow system to recover
	dfs.writeMetadata(meta)

	// commit changes to disk
	if e = file.Sync(); e != nil {
		// abandon write
		file.Close()
		os.Remove(dfs.getFullFilePath(fname))

		// sync never happenned, so no need for log; delete it
		dfs.resetMetadata()

		// send an ack with an error
		if e = dfs.client.Call("DFSServer.AckOpen", ackArgs, &ackReply); e != nil {
			return nil, DisconnectedError(dfs.serverAddr)
		}

		return nil, DisconnectedError(dfs.serverAddr)
	}

	dfs.updateVersions(fname, versions)

	// Ack that the open has succeeded
	ackArgs.Val = 0
	if e = dfs.client.Call("DFSServer.AckOpen", ackArgs, &ackReply); e != nil {
		// AckOpen failed, so go back to original file state
		dfs.recoverOpen(meta.MetaOpen)
		// now delete the log
		dfs.resetMetadata()
		return nil, DisconnectedError(dfs.serverAddr)
	}

	// delete log
	dfs.resetMetadata()

	// dfsFile := &DFSFileT{file: file, fname: fname, fileMode: mode, dfs: dfs, disconnected: false, closed: false, versions: versions}
	// dfs.files = append(dfs.files, dfsFile)
	dfs.files[fname].file = file
	dfs.files[fname].versions = versions
	return dfs.files[fname], nil
}

func (dfs *DFST) UMountDFS() (err error) {
	if !dfs.connected {
		// try to disconnect just in case
		if dfs.client != nil {
			dfs.client.Close()
		}
		return DisconnectedError(dfs.serverAddr)
	}

	// stop heartbeat
	dfs.stopHeartbeatChan <- true

	// close local fds
	for _, dfsFile := range dfs.files {
		dfsFile.file.Close()
	}

	args := &communication.UMountClientArgs{Name: dfs.name}
	var reply int
	if err = dfs.client.Call("DFSServer.UMountClient", args, &reply); err != nil {
		if dfs.client != nil {
			dfs.client.Close()
		}
		return DisconnectedError(dfs.serverAddr)
	}

	if dfs.client != nil {
		dfs.client.Close()
	}
	return nil
}

func (dfs *DFST) writeMetadata(metaObj metadata) {
	metaPath := filepath.Join(dfs.localPath, "metadata")
	metaFile, _ := os.OpenFile(metaPath, os.O_RDWR|os.O_CREATE, 0666)
	meta, _ := json.Marshal(metaObj)
	metaFile.Truncate(int64(len(meta)))
	metaFile.Write(meta)
	metaFile.Sync()
	metaFile.Close()
}

func (dfs *DFST) resetMetadata() {
	dfs.writeMetadata(metadata{Name: dfs.name, State: 0})
}

func (dfs *DFST) readVersions(fname string) [256]int {
	versionsPath := filepath.Join(dfs.localPath, fname)
	content, _ := ioutil.ReadFile(versionsPath)
	var versions dFSFileVersions
	json.Unmarshal(content, &versions)
	return versions.Versions
}

// updates file's version at specific entry
func (dfs *DFST) updateVersion(fname string, chunkNum uint8, version int) {
	versions := dfs.readVersions(fname)
	versions[chunkNum] = version

	versionsPath := filepath.Join(dfs.localPath, fname)
	// file should already exist
	versionsFile, e := os.OpenFile(versionsPath, os.O_RDWR, 0666)
	if e != nil {
		return
	}

	versionsArr, _ := json.Marshal(dFSFileVersions{versions})
	versionsFile.Truncate(int64(len(versionsArr)))
	versionsFile.Write(versionsArr)
	versionsFile.Sync()
	versionsFile.Close()
}

func (dfs *DFST) updateVersions(fname string, versions [256]int) {
	versionsPath := filepath.Join(dfs.localPath, fname)
	versionsFile, _ := os.OpenFile(versionsPath, os.O_RDWR|os.O_CREATE, 0666)
	versionsArr, _ := json.Marshal(dFSFileVersions{versions})
	versionsFile.Truncate(int64(len(versionsArr)))
	versionsFile.Write(versionsArr)
	versionsFile.Sync()
	versionsFile.Close()
}

// sends heartbeat message to server for given name client ever 0.5 seconds
func (dfs *DFST) clientHeartbeat() {
	conn, err := net.Dial("udp", dfs.serverAddr)
	defer conn.Close()
	defer close(dfs.stopHeartbeatChan)

	if err != nil {
		return
	}

	heartbeat, _ := json.Marshal(communication.HeartbeatMessage{Name: dfs.name})
	for {
		select {
		default:
			conn.Write(heartbeat)
			time.Sleep(time.Millisecond * 500)
		case <-dfs.stopHeartbeatChan:
			return
		}
	}
}

// reads metadata file and tries to recover from any crash
// should be called on MountDFS, and Open if going from disconnected->connected
// should be done before clientRPC is setup, and before official connection is made with server,
// to avoid concurrency issues
func (dfs *DFST) recover() {
	metaPath := filepath.Join(dfs.localPath, "metadata")
	// read metadata file
	_, e := os.Stat(metaPath)
	if e != nil {
		// file does not exist; just return
		return
	}

	// metadata file exists
	content, e := ioutil.ReadFile(metaPath)
	// for errors, just return
	if e != nil {
		return
	}

	var meta metadata
	e = json.Unmarshal(content, &meta)
	if e != nil {
		return
	}

	dfs.name = meta.Name

	// recover from crash states
	if meta.State == 0 {
		// no crashes to recover from
		return
	} else if meta.State == 1 {
		// crashed during a write; try redoing write
		// get version number
		chunkNum := meta.MetaRW.ChunkNum
		fname := meta.MetaRW.FName
		args := &communication.VersionCheckArgs{
			CName:    dfs.name,
			FName:    fname,
			ChunkNum: chunkNum}
		var reply communication.VersionCheckReply
		e = dfs.client.Call("DFSServer.VersionCheck", args, &reply)
		if e != nil {
			// client is disconnected; just return
			return
		} else {
			curVersions := dfs.readVersions(fname)
			if reply.Version > curVersions[chunkNum] {
				// write was successful server-side
				// finish write
				chunk := meta.MetaRW.ChunkData

				file, _ := os.OpenFile(dfs.getFullFilePath(fname), os.O_RDWR, 0666)
				file.WriteAt(chunk[:], int64(len(chunk)*int(chunkNum)))
				file.Sync()
				file.Close()

				// update the version on disk
				dfs.updateVersion(fname, chunkNum, reply.Version)

				// delete log
				dfs.resetMetadata()
			} else {
				// write was not successful server-side, so just delete the log
				dfs.resetMetadata()
			}
		}
	} else if meta.State == 2 {
		// crashed during a read; rollback read and update server
		chunkNum := meta.MetaRW.ChunkNum
		fname := meta.MetaRW.FName
		// get version number
		curVersions := dfs.readVersions(fname)
		version := curVersions[chunkNum]

		// write back original
		chunk := meta.MetaRW.ChunkData
		file, _ := os.OpenFile(dfs.getFullFilePath(fname), os.O_RDWR, 0666)
		file.WriteAt(chunk[:], int64(len(chunk)*int(chunkNum)))
		file.Sync()
		file.Close()

		// update the version on disk
		dfs.updateVersion(fname, chunkNum, version)

		// update the server with the old version
		args := &communication.VersionUpdateArgs{
			CName:    dfs.name,
			FName:    fname,
			Versions: curVersions}
		var reply communication.VersionUpdateReply
		e = dfs.client.Call("DFSServer.VersionUpdate", args, &reply)
		if e != nil {
			// client is disconnected; just return
			return
		} else {
			// delete log
			dfs.resetMetadata()
		}
	} else if meta.State == 3 {
		dfs.recoverOpen(meta.MetaOpen)
		fname := meta.MetaOpen.FName

		if meta.MetaOpen.New {
			// file was new; need to delete record of having file on server
			args := &communication.CloseArgs{
				CName: dfs.name,
				FName: fname}
			var reply int
			e = dfs.client.Call("DFSServer.DeleteRecord", args, &reply)
			if e != nil {
				// client is disconnected; just return
				return
			} else {
				// delete log
				dfs.resetMetadata()
			}
		} else {
			// update server's record of which files this client has
			args := &communication.VersionUpdateArgs{
				CName:    dfs.name,
				FName:    fname,
				Versions: meta.MetaOpen.Versions}
			var reply communication.VersionUpdateReply
			e = dfs.client.Call("DFSServer.VersionUpdate", args, &reply)
			if e != nil {
				// client is disconnected; just return
				return
			} else {
				// delete log
				dfs.resetMetadata()
			}
		}
	}
}

func (dfs *DFST) recoverOpen(metaOpen metadataOpen) {
	if metaOpen.New {
		// file was new; delete file and versions
		filePath := dfs.getFullFilePath(metaOpen.FName)
		if _, e := os.Stat(filePath); e == nil {
			os.Remove(filePath)
		}
		versionsPath := filepath.Join(dfs.localPath, metaOpen.FName)
		if _, e := os.Stat(versionsPath); e == nil {
			os.Remove(versionsPath)
		}
	} else {
		// file was not new; overwrite it with logged data
		file, _ := os.Open(dfs.getFullFilePath(metaOpen.FName))
		for i := 0; i < 256; i++ {
			file.WriteAt(metaOpen.File[i][:], int64(len(metaOpen.File[i])*int(i)))
		}
		dfs.updateVersions(metaOpen.FName, metaOpen.Versions)
	}
}

func (dfs *DFST) reconnect() (err error) {
	var e error
	if dfs.client, e = rpc.Dial("tcp", dfs.serverAddr); e != nil {
		return DisconnectedError(dfs.serverAddr)
	}

	var files []string
	// add still open files to list
	for k, _ := range dfs.files {
		files = append(files, k)
	}

	args := &communication.ReconnectArgs{CName: dfs.name, Files: files}
	var reply int
	if e = dfs.client.Call("DFSServer.Reconnect", args, &reply); e != nil {
		return DisconnectedError(dfs.serverAddr)
	}
	dfs.connected = true
	return nil
}

// Idea for singleton implementation based off https://stackoverflow.com/questions/1823286/singleton-in-go
// pointer to singleton dfs instance
var dfst *DFST
var once sync.Once

// The constructor for a new DFS object instance. Takes the server's
// IP:port address string as parameter, the localIP to use to
// establish the connection to the server, and a localPath path on the
// local filesystem where the client has allocated storage (and
// possibly existing state) for this DFS.
//
// The returned dfs instance is singleton: an application is expected
// to interact with just one dfs at a time.
//
// This call should succeed regardless of whether the server is
// reachable. Otherwise, applications cannot access (local) files
// while disconnected.
//
// Can return the following errors:
// - LocalPathError
// - Networking errors related to localIP or serverAddr
func MountDFS(serverAddr string, localIP string, localPath string) (dfs DFS, err error) {
	fmt.Println("MountDFS")
	var e error
	if _, e = os.Stat(localPath); e != nil {
		// localPath does not exist
		return nil, LocalPathError(localPath)
	}

	// make thread-safe singleton intialization
	once.Do(func() {
		dfst = &DFST{}
	})

	dfst.connected = false
	dfst.serverAddr = serverAddr
	dfst.localPath = localPath
	dfst.name = -1
	dfst.files = make(map[string]*DFSFileT)

	// write metadata before closing
	defer func(dfs *DFST) {
		dfs.resetMetadata()
	}(dfst)

	if dfst.client, e = rpc.Dial("tcp", serverAddr); e != nil {
		fmt.Println("1")
		fmt.Println(e)
		return dfst, DisconnectedError(serverAddr)
	}

	dfst.recover()

	// Setup ClientRPC
	server := rpc.NewServer()
	clientRPC := &ClientRPC{dfs: dfst}
	server.Register(clientRPC)
	l, e := net.Listen("tcp", localIP+":0")
	if e != nil {
		fmt.Println("2")
		return dfst, DisconnectedError(serverAddr)
	}
	go server.Accept(l)

	dfst.localAddr = l.Addr().String()

	// create a new client connection
	args := &communication.MountClientArgs{Name: dfst.name, Address: dfst.localAddr}
	var reply int
	if e = dfst.client.Call("DFSServer.MountClient", args, &reply); e != nil {
		fmt.Println("3")
		return dfst, DisconnectedError(serverAddr)
	}
	dfst.name = reply

	// Now that all the connection has been set up successfully, set
	// connected to be true
	dfst.connected = true

	dfst.stopHeartbeatChan = make(chan bool, 1)

	// start sending heartbeat messages for this file
	go dfst.clientHeartbeat()

	return dfst, nil
}
