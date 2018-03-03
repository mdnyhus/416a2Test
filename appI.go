// CS416 A2 Testing File

package main

import (
	"./dfslib"

	"fmt"
	"os"
	"strings"
	"bufio"
	"strconv"
)


func main() {
	localIP := os.Args[1]
	serverAddr := os.Args[2]
	lastIP := localIP
	// These IPs work for my setup, but you may want to change them
	// localip := "127.0.0." + lastIP // you may want to change this when testing
	// serverAddr := "127.0.0.1:8080"

	var err error

	localPath := "./clientInteractive" + lastIP
	if _, err = os.Stat(localPath); os.IsNotExist(err) {
	    os.Mkdir(localPath, os.ModePerm)
	}

	var dfs dfslib.DFS
	files := make(map[string]dfslib.DFSFile)

	fmt.Println("Interactive Client with localIP " + localIP)
	fmt.Println("\tCommands:")
	fmt.Println("\t\tMountDFS")
	fmt.Println("\t\tUMountDFS")
	fmt.Println("\t\tOpen [fname] [mode]")
	fmt.Println("\t\tRead [fname] [chunkNum]")
	fmt.Println("\t\tWrite [fname] [chunkNum] [chunkData]")
	fmt.Println("\t\tClose [fname]")
	fmt.Println("\t\tLocalFileExists [fname]")
	fmt.Println("\t\tGlobalFileExists [fname]")
	fmt.Println("\t\tQuit")

	for {
		err = nil
		exists := false
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Enter command: ")
		text, _ := reader.ReadString('\n')
		words := strings.Fields(text)
		var blob dfslib.Chunk

		switch words[0] {
		case "MountDFS":
			dfs, err = dfslib.MountDFS(serverAddr, localIP, localPath)
		case "UMountDFS":
			err = dfs.UMountDFS()
		case "Open":
			if len(words) < 3 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				mode, _ := strconv.Atoi(words[2])
				var file dfslib.DFSFile
				file, err = dfs.Open(fname, dfslib.FileMode(mode))
				if err == nil {
					files[fname] = file
				}
			}
		case "Read":
			if len(words) < 3 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				file, ok := files[fname]
				if !ok {
					fmt.Println("file " + fname + " is not open")
				} else {
					chunknum, _ := strconv.Atoi(words[2])
					err = file.Read(uint8(chunknum), &blob)
					if err == nil {
						fmt.Println(blob)
					}
				}
			}
		case "Write":
			if len(words) < 4 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				file, ok := files[fname]
				if !ok {
					fmt.Println("file " + fname + " is not open")
				} else {
					chunknum, _ := strconv.Atoi(words[2])
					copy(blob[:], words[3])
					err = file.Write(uint8(chunknum), &blob)
				}
			}
		case "Close":
			if len(words) < 2 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				file, ok := files[fname]
				if !ok {
					fmt.Println("file " + fname + " is not open")
				} else {
					err = file.Close()
					delete(files, fname)
				}
			}
		case "LocalFileExists":
			if len(words) < 2 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				exists, err = dfs.LocalFileExists(fname)
				if exists {
					fmt.Printf("%s exists locally\n", (fname))
				} else {
					fmt.Printf("%s does not exist locally\n", (fname))
				}
			}
		case "GlobalFileExists":
			if len(words) < 2 {
				fmt.Println("Not enough arguments")
			} else {
				fname := words[1]
				exists, err = dfs.GlobalFileExists(fname)
				if exists {
					fmt.Printf("%s exists globally\n", (fname))
				} else {
					fmt.Printf("%s does not exist globally\n", (fname))
				}
			}
		case "Quit":
			return
		default:
			fmt.Println("\tInvalid command")
		}

		if err != nil {
			fmt.Println("error")
			fmt.Println(err)
		}
	}

	return
}