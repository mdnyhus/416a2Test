/*
Implements the server for assignment 2 for UBC CS 416 2017 W2.

Usage:
$ go run server.go [client-incoming ip:port]

Example:
$ go run server.go 127.0.0.1:2020

*/

package main

import (
	"fmt"
	"net"
	"net/rpc"
	// "errors"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"./communication"
)

type ClientFile struct {
	name                 string
	chunkVersions        [256]int
	pendingChunkVersions [256]int
}

type GlobalFile struct {
	writer int
	// this mutex controls who the writer of the file is,
	// as well as the chunkVersions for the corresponding ClientFiles
	globalFileMutex sync.Mutex
}

type Client struct {
	// For convenience, this will be in the index in the
	// DFSServer's client table, so must be from 0-15
	name              int
	connected         bool
	timeoutDisconnect bool
	address           string
	cliChan           chan int
	clientRPC         *rpc.Client

	files               map[string]*ClientFile
	filesWriting        []string
	filesTimeoutWriting []string
	clientMutex         sync.Mutex
}

type DFSServer struct {
	address string
	// at most 16 clients will connect
	clients [16]*Client
	files   map[string]*GlobalFile
}

// should be run with goroutine, waits for heartbeats from app
// if no heartbeat received for 2 seconds, then disconnects client
func (dfsServer *DFSServer) receiveHeartbeat() {
	// create local udp addr socket
	serverUDPAddr, e := net.ResolveUDPAddr("udp", dfsServer.address)
	if e != nil {
		return
	}

	serverUDPConn, e := net.ListenUDP("udp", serverUDPAddr)
	if e != nil {
		return
	}

	buffer := make([]byte, 1024)

	for {
		size, _, err := serverUDPConn.ReadFromUDP(buffer)
		go dfsServer.handleHeartbeat(buffer[:size], err)
	}
}

func (dfsServer *DFSServer) handleHeartbeat(buffer []byte, err error) {
	if err != nil {
		return
	}

	// parse client name from heartbeat
	var heartbeatMessage communication.HeartbeatMessage
	err = json.Unmarshal(buffer, &heartbeatMessage)
	if err != nil {
		return
	}

	// make sure the client is connected
	if dfsServer.clients[heartbeatMessage.Name].connected {
		// channel must be open if client is connected
		dfsServer.clients[heartbeatMessage.Name].cliChan <- 0
	}
}

func (dfsServer *DFSServer) clientTimeout(name int) {
	// allow some setup time
	time.Sleep(time.Second)

	end := false
	for !end {
		select {
		case i := <-dfsServer.clients[name].cliChan:
			// take "-1" to be a close
			if i == -1 {
				end = true
				dfsServer.clients[name].timeoutDisconnect = false
				dfsServer.cleanup(name)
			}
		case <-time.After(time.Second * 2):
			end = true
			dfsServer.clients[name].timeoutDisconnect = true
			dfsServer.cleanup(name)
		}
	}
}

func (dfsServer *DFSServer) cleanup(name int) {
	client := dfsServer.clients[name]
	client.connected = false
	// only close channel after connected is false
	close(client.cliChan)
	// close RPC
	if client.clientRPC != nil {
		client.clientRPC.Close()
	}

	// release files opened as a writer
	client.clientMutex.Lock()

	if client.timeoutDisconnect {
		// if cleanup is result of timeout, then save list of writing files
		client.filesTimeoutWriting = client.filesWriting
	} else {
		// if not, reset the list
		client.filesTimeoutWriting = []string{}
	}

	for i := 0; i < len(client.filesWriting); i++ {
		// release each file one by one
		globalFile, ok := dfsServer.files[client.filesWriting[i]]
		if ok {
			globalFile.globalFileMutex.Lock()
			// double check this client was actually the writer
			if globalFile.writer == name {
				// release writer
				globalFile.writer = -1
			}
			globalFile.globalFileMutex.Unlock()
		}
	}

	// reset filesWriting array
	client.filesWriting = []string{}
	client.clientMutex.Unlock()
}

func validName(name int) bool {
	return name >= 0 && name < 16
}

func (dfsServer *DFSServer) MountClient(args *communication.MountClientArgs, reply *int) (err error) {
	fmt.Println("hello")
	// reply is -1 on error
	*reply = -1

	name := args.Name
	if name != -1 && !validName(name) {
		// invalid name
		// only error is disconnected error
		// return errors.New("invalid name - out of range")
		fmt.Println("1")
		return communication.DisconnectedError(args.Address)
	}

	// name is valid
	if name == -1 {
		// create a new client
		// find first nil client
		for i := 0; i < 16; i++ {
			if dfsServer.clients[i] == nil {
				dfsServer.clients[i] = &Client{name: i, files: make(map[string]*ClientFile)}
				// setup client

				if e := dfsServer.setupClient(i, args.Address, true, []string{}); e != nil {
					return e
				} else {
					// reply with name
					*reply = i
					return nil
				}
			}
		}
		// If no nil client was found, more than 16 clients trying to connect, error
		// only error is disconnected error
		// return errors.New("too many clients")
	}

	// name is in valid range
	client := dfsServer.clients[name]
	if client == nil {
		// trying to restart client that was never started in the first place
		// only error is disconnected error
		// return errors.New("invalid name - never connected")
		fmt.Println("2")
		return communication.DisconnectedError(args.Address)
	} else if client.connected {
		// client is currently connected; invalid name
		// only error is disconnected error
		// return errors.New("invalid name - already connected")
		fmt.Println("3")
		return communication.DisconnectedError(args.Address)
	}

	// at this point, the name points to a valid, disconnected, already-initialized client
	if e := dfsServer.setupClient(name, args.Address, true, []string{}); e != nil {
		return e
	} else {
		// reply with name
		*reply = name
		return nil
	}
}

func (dfsServer *DFSServer) setupClient(name int, address string, all bool, filesOpen []string) (err error) {
	client := dfsServer.clients[name]
	if client == nil {
		// client not initialized
		// only error is disconnected error
		return communication.DisconnectedError(address)
	}

	// get lock
	client.clientMutex.Lock()

	// try to get write access back on any files that it had
	for i := 0; i < len(client.filesTimeoutWriting); i++ {
		// check if file is still open
		stillOpen := all
		for j := 0; j < len(filesOpen); j++ {
			if filesOpen[j] == client.filesTimeoutWriting[i] {
				stillOpen = true
				break
			}
		}

		if !stillOpen {
			// file was closed while disconnected
			client.filesTimeoutWriting = append(client.filesTimeoutWriting[:i], client.filesTimeoutWriting[i+1:]...)
			// decremenet index so we don't skip an element
			i--
			continue
		}

		// get global file mutex
		globalFile, ok := dfsServer.files[client.filesTimeoutWriting[i]]
		if !ok {
			// file does not exist; remove it for the slice
			client.filesTimeoutWriting = append(client.filesTimeoutWriting[:i], client.filesTimeoutWriting[i+1:]...)
			// decremenet index so we don't skip an element
			i--
			continue
		}

		globalFile.globalFileMutex.Lock()
		if globalFile.writer == -1 {
			// give write access back to client
			globalFile.writer = name
			client.filesWriting = append(client.filesWriting, client.filesTimeoutWriting[i])
			// remove it for filesTimeoutWriting
			client.filesTimeoutWriting = append(client.filesTimeoutWriting[:i], client.filesTimeoutWriting[i+1:]...)
			// decremenet index so we don't skip an element
			i--
		}

		globalFile.globalFileMutex.Unlock()
	}

	// address = "" means no change to address at client
	if address != "" {
		client.address = address
	}
	client.cliChan = make(chan int, 1)

	// setup struct after creating channel
	client.connected = true
	client.timeoutDisconnect = false

	// setup heartbeat timout
	go dfsServer.clientTimeout(name)

	// setup RPC connection with client
	if client.clientRPC, err = rpc.Dial("tcp", client.address); err != nil {
		// release lock
		client.clientMutex.Unlock()
		return communication.DisconnectedError(address)
	}

	// release lock
	client.clientMutex.Unlock()

	return nil
}

func (dfsServer *DFSServer) UMountClient(args *communication.UMountClientArgs, reply *int) (err error) {
	client := dfsServer.clients[args.Name]
	if client.connected {
		// client already disconnected
		return nil
	}

	// close clientTimeout by sending -1 to channel; this will cleanup dependencies
	client.cliChan <- -1
	return nil
}

// used for sort that remember indices
type NameScore struct {
	name  int
	score int
}

func (dfsServer *DFSServer) Open(args *communication.OpenArgs, reply *communication.OpenReply) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", true, []string{})
	}

	globalFile, ok := dfsServer.files[args.FName]
	if !ok {
		globalFile = &GlobalFile{writer: -1}
		dfsServer.files[args.FName] = globalFile
	}

	// check writer
	if args.Mode == communication.WRITE {
		globalFile.globalFileMutex.Lock()
		// why && globalFile.writer != args.CName?
		if globalFile.writer != -1 && globalFile.writer != args.CName {
			globalFile.globalFileMutex.Unlock()
			// OpenWriteConflictError
			reply.ErrType = 1
			return nil
		} else {
			// client can be the writer
			globalFile.writer = args.CName
			globalFile.globalFileMutex.Unlock()
			// add file to client's filesWriting
			client := dfsServer.clients[args.CName]
			client.clientMutex.Lock()
			client.filesWriting = append(client.filesWriting, args.FName)
			client.clientMutex.Unlock()
		}
	}

	clientFile, ok := dfsServer.clients[args.CName].files[args.FName]
	if !ok {
		clientFile = &ClientFile{name: args.FName}
		dfsServer.clients[args.CName].files[args.FName] = clientFile
	}

	// need to know if read version is non-trivial
	// and need to know if file is non-trivial
	fileTrivial := true
	sendTrivial := true

chunkLoop:
	for i := 0; i < len(clientFile.chunkVersions); i++ {
		clientVersion := clientFile.chunkVersions[i]

		versions := [16]NameScore{}
		// iterate through all possible clients
		// this should be fast enough, since there are only a max of 16 clients
		// for concurrency, know that the version can only go up
		for j := 0; j < 16; j++ {
			versions[j].name = j
			client := dfsServer.clients[j]
			if client != nil {
				file, ok := client.files[args.FName]
				if ok {
					versions[j].score = file.chunkVersions[i]
				}
			}
		}

		sort.Slice(versions[:], func(i, j int) bool {
			return versions[i].score > versions[j].score
		})

		maxVersion := versions[0].score
		if maxVersion > 0 {
			// at least one chunk in the file is non-trivial, so entire file
			// is non-trivial
			fileTrivial = false
		}

		for j := 0; j < 16; j++ {
			version := versions[j].score
			name := versions[j].name
			if clientVersion >= version || version == 0 {
				if clientVersion >= maxVersion && clientVersion != 0 {
					// if clientVersion is most up-to-date and non-trivial, then by default
					// we can send a non-trivial version of the file
					sendTrivial = false
				}

				// otherwise, callee client is already up to date for this chunk
				reply.FileData[i].New = false
				reply.FileData[i].Version = version
				clientFile.pendingChunkVersions[i] = version
				continue chunkLoop

			} else if dfsServer.clients[name] != nil && dfsServer.clients[name].connected {
				// client name has a more up to date version and is connected
				readArgs := &communication.ReadArgs{ChunkNum: uint8(i), FName: args.FName}
				var readReply communication.ReadReply
				e := dfsServer.clients[name].clientRPC.Call("ClientRPC.ReadRPC", readArgs, &readReply)
				// if there is no error, then use the received chunk
				// (if there was an error during the call, just try another client)
				if e == nil && readReply.ErrType == 0 {
					reply.FileData[i].New = true
					reply.FileData[i].Version = version
					reply.FileData[i].ChunkData = readReply.ChunkData

					// update client's pending version of this file
					clientFile.pendingChunkVersions[i] = version

					// read was successful
					// file we're sending is no longer trivial, since version != 0
					sendTrivial = false

					continue chunkLoop
				}
			}
		}
	}

	if !fileTrivial && sendTrivial {
		// the file is not trivial, but what we're sending is trivial
		// this is a FileUnavailableError
		reply.ErrType = 2

		// remove writer control
		globalFile.globalFileMutex.Lock()
		if args.Mode == communication.WRITE && globalFile.writer == args.CName {
			globalFile.writer = -1
			globalFile.globalFileMutex.Unlock()
			// remove file from any writing slice
			client := dfsServer.clients[args.CName]
			client.clientMutex.Lock()
			for i, fname := range client.filesWriting {
				// find file in client's filesWriting
				if fname == args.FName {
					// remove it
					client.filesWriting = append(client.filesWriting[:i], client.filesWriting[i+1:]...)
					break
				}
			}
			client.clientMutex.Unlock()
		}
	}
	return nil
}

// Called so the server can update the client's version number
func (dfsServer *DFSServer) AckOpen(args *communication.AckOpenArgs, reply *int) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", true, []string{})
	}

	*reply = 0
	clientFile, ok := dfsServer.clients[args.CName].files[args.FName]
	if !ok {
		return communication.DisconnectedError(dfsServer.address)
	}

	// only update on successful ack
	if args.Val == 0 {
		dfsServer.clients[args.CName].clientMutex.Lock()
		clientFile.chunkVersions = clientFile.pendingChunkVersions
		dfsServer.clients[args.CName].clientMutex.Unlock()
	} else {
		// if file was the writer fo this file and write was not acknowledged successfully,
		// then release the writer
		globalFile, ok := dfsServer.files[args.FName]
		if !ok {
			return communication.DisconnectedError(dfsServer.address)
		}

		globalFile.globalFileMutex.Lock()
		if globalFile.writer == args.CName {
			globalFile.writer = -1
		}
		globalFile.globalFileMutex.Unlock()

	}

	// reset pendingChunkVersions
	clientFile.pendingChunkVersions = [256]int{}

	return nil
}

func (dfsServer *DFSServer) FileExists(args *communication.FileExistsArgs, reply *bool) (err error) {
	_, *reply = dfsServer.files[args.FName]
	return nil
}

func (dfsServer *DFSServer) Read(args *communication.ReadArgs, reply *communication.ReadReply) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", true, []string{})
	}

	// similar code to part of open
	versions := [16]NameScore{}
	// iterate through all possible clients
	// this should be fast enough, since there are only a max of 16 clients
	// for concurrency, know that the version can only go up
	for j := 0; j < 16; j++ {
		versions[j].name = j
		client := dfsServer.clients[j]
		if client != nil {
			file, ok := client.files[args.FName]
			if ok {
				versions[j].score = file.chunkVersions[args.ChunkNum]
			}
		}
	}

	sort.Slice(versions[:], func(i, j int) bool {
		return versions[i].score > versions[j].score
	})

	maxVersion := versions[0].score

	for j := 0; j < 16; j++ {
		version := versions[j].score
		name := versions[j].name
		if dfsServer.clients[name] != nil && dfsServer.clients[name].connected && version == maxVersion {
			e := dfsServer.clients[name].clientRPC.Call("ClientRPC.ReadRPC", args, reply)
			// if there is no error, then use the received chunk
			// (if there was an error during the call, just try another client)
			if e == nil {
				reply.Version = version
				return nil
			}
		}
	}

	// ChunkUnavailableError
	reply.ErrType = 1
	return nil
}

func (dfsServer *DFSServer) Write(args *communication.WriteArgs, reply *communication.WriteReply) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", true, []string{})
	}

	globalFile, ok := dfsServer.files[args.FName]
	if !ok {
		// this case should never happen
		// BadFileModeError
		reply.ErrType = 1
		return nil
	}

	if globalFile.writer != args.CName {
		// see if client still has write access
		// this should only be due to a timeout, in which case the file should be in
		// the client's filesTimeoutWritin
		// WriteModeTimeoutError
		reply.ErrType = 2
		return nil
	}

	globalFile.globalFileMutex.Lock()

	maxVersion := 0
	for j := 0; j < 16; j++ {
		client := dfsServer.clients[j]
		if client != nil {
			file, ok := client.files[args.FName]
			if ok {
				if file.chunkVersions[args.ChunkNum] > maxVersion {
					maxVersion = file.chunkVersions[args.ChunkNum]
				}
			}
		}
	}

	newVersion := maxVersion + 1
	file, ok := dfsServer.clients[args.CName].files[args.FName]
	if ok {
		// update version number
		file.chunkVersions[args.ChunkNum] = newVersion
		reply.Version = newVersion
		err = nil
	} else {
		// this case should never happen
		// BadFileModeError
		reply.ErrType = 1
		err = nil
	}

	globalFile.globalFileMutex.Unlock()

	return err
}

func (dfsServer *DFSServer) VersionCheck(args *communication.VersionCheckArgs, reply *communication.VersionCheckReply) (err error) {
	file, ok := dfsServer.clients[args.CName].files[args.FName]
	if ok {
		// return version number
		reply.Version = file.chunkVersions[args.ChunkNum]
		return nil
	} else {
		// this case should never happen
		// BadFilenameError
		reply.ErrType = 1
		return nil
	}
}

func (dfsServer *DFSServer) VersionUpdate(args *communication.VersionUpdateArgs, reply *communication.VersionUpdateReply) (err error) {
	file, ok := dfsServer.clients[args.CName].files[args.FName]
	if ok {
		// return version number
		dfsServer.clients[args.CName].clientMutex.Lock()
		file.chunkVersions = args.Versions
		dfsServer.clients[args.CName].clientMutex.Unlock()
		return nil
	} else {
		// this case should never happen
		// BadFilenameError
		reply.ErrType = 1
		return nil
	}
}

// delete's server's record that client has file on disk
func (dfsServer *DFSServer) DeleteRecord(args *communication.CloseArgs, reply *int) (err error) {
	// if client was writer, release writer control
	globalFile, ok := dfsServer.files[args.FName]
	if ok {
		globalFile.globalFileMutex.Lock()
		if globalFile.writer == args.CName {
			globalFile.writer = -1
		}
		globalFile.globalFileMutex.Unlock()
	}

	client := dfsServer.clients[args.CName]
	client.clientMutex.Lock()
	// remove record of file
	delete(client.files, args.FName)
	client.clientMutex.Unlock()

	return nil
}

func (dfsServer *DFSServer) Close(args *communication.CloseArgs, reply *int) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", true, []string{})
	}

	client := dfsServer.clients[args.CName]
	client.clientMutex.Lock()

	globalFile, _ := dfsServer.files[args.FName]
	globalFile.globalFileMutex.Lock()
	if globalFile.writer == args.CName {
		globalFile.writer = -1
		globalFile.globalFileMutex.Unlock()

		// remove file from any writing slice
		for i, fname := range client.filesWriting {
			// find file in client's filesWriting
			if fname == args.FName {
				// remove it
				client.filesWriting = append(client.filesWriting[:i], client.filesWriting[i+1:]...)
				break
			}
		}

		for i, fname := range client.filesTimeoutWriting {
			// find file in client's filesTimeoutWriting
			if fname == args.FName {
				// remove it
				client.filesTimeoutWriting = append(client.filesTimeoutWriting[:i], client.filesTimeoutWriting[i+1:]...)
				break
			}
		}
	} else {
		globalFile.globalFileMutex.Unlock()
	}
	client.clientMutex.Unlock()

	return nil
}

func (dfsServer *DFSServer) Reconnect(args *communication.ReconnectArgs, reply *int) (err error) {
	if !dfsServer.clients[args.CName].connected {
		// client was disconnected, so reconnect
		dfsServer.setupClient(args.CName, "", false, args.Files)
	}
	return nil
}

func main() {
	// skip program
	args := os.Args[1:]

	numArgs := 1

	// check number of arguments
	if len(args) != numArgs {
		if len(args) < numArgs {
			fmt.Printf("too few arguments; expected %d, received%d\n", numArgs, len(args))
		} else {
			fmt.Printf("too many arguments; expected %d, received%d\n", numArgs, len(args))
		}
		// can't proceed without correct number of arguments
		return
	}

	server := rpc.NewServer()
	dfsServer := &DFSServer{address: args[0], files: make(map[string]*GlobalFile)}
	server.Register(dfsServer)
	l, e := net.Listen("tcp", dfsServer.address)
	if e != nil {
		return
	}
	go server.Accept(l)
	go dfsServer.receiveHeartbeat()

	fmt.Println("accepting")

	for {

	}
}
