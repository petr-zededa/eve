// Copyright(c) 2017-2018 Zededa, Inc.
// All rights reserved.

package sftp

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	SingleMB int64 = 1024 * 1024
)

// stats update
type UpdateStats struct {
	Size          int64    // complete size to upload/download
	Asize         int64    // current size uploaded/downloaded
	List          []string //list of images at given path
	Error         error
	ContentLength int64
}

type NotifChan chan UpdateStats

func getSftpClient(host, user, pass string) (*sftp.Client, error) {
	clientConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
			ssh.KeyboardInteractive(
				func(user, instruction string, questions []string, echos []bool) ([]string, error) {
					answers := make([]string, len(questions))
					for i := range answers {
						answers[i] = pass
					}
					return answers, nil
				}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(10) * time.Second,
	}

	// We break this up into a DNS lookup and a Dial only to be able to detect
	// the errors better
	args := strings.Split(host, ":")
	if _, err := net.LookupHost(args[0]); err != nil {
		log.Printf("LookupHost error: %s", err)
		return nil, err
	}
	client, err := ssh.Dial("tcp", host, clientConfig)
	if err != nil {
		return nil, err
	}
	session, err := sftp.NewClient(client)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func ExecCmd(cmd, host, user, pass, remoteFile, localFile string,
	objSize int64, prgNotify NotifChan) UpdateStats {

	var list []string
	stats := UpdateStats{}
	client, err := getSftpClient(host, user, pass)
	if err != nil {
		stats.Error = fmt.Errorf("sftpclient failed for %s: %s",
			host, err)
		return stats
	}
	defer client.Close()
	switch cmd {
	case "ls":
		walker := client.Walk(remoteFile)
		for walker.Step() {
			if err := walker.Err(); err != nil {
				stats.Error = err
				return stats
			}
			file := strings.Replace(walker.Path(), remoteFile+"/", "", -1)
			list = append(list, file)
		}
		stats.List = list
		if prgNotify != nil {
			select {
			case prgNotify <- stats:
			default: //ignore we cannot write
			}
		}
		return stats
	case "fetch":
		fr, err := client.Open(remoteFile)
		if err != nil {
			stats.Error = fmt.Errorf("open failed for %s: %s",
				remoteFile, err)
			return stats
		}
		defer fr.Close()
		tempLocalFile := localFile
		index := strings.LastIndex(tempLocalFile, "/")
		dir := tempLocalFile[:index+1]
		if err := os.MkdirAll(dir, 0755); err != nil {
			stats.Error = fmt.Errorf("cannot create dir %s: %s", dir, err)
			return stats
		}

		fl, err := os.Create(localFile)
		if err != nil {
			stats.Error = fmt.Errorf("cannot create file %s: %s", localFile, err)
			return stats
		}
		defer fl.Close()

		chunkSize := SingleMB
		var written int64
		stats.Size = objSize
		for {
			if written, err = io.CopyN(fl, fr, chunkSize); err != nil && err != io.EOF {
				stats.Error = err
				return stats
			}
			stats.Asize += written //we should process all chunks, not only divisible by chunkSize
			if prgNotify != nil {
				select {
				case prgNotify <- stats:
				default: //ignore we cannot write
				}
			}
			if written != chunkSize {
				// Must have reached EOF
				break
			}
		}
		return stats
	case "put":
		tempRemoteFile := remoteFile
		index := strings.LastIndex(tempRemoteFile, "/")
		err := client.MkdirAll(tempRemoteFile[:index+1])
		if err != nil {
			stats.Error = fmt.Errorf("mkdir failed for %s: %s",
				tempRemoteFile[:index+1], err)
			return stats
		}
		fr, err := client.Create(remoteFile)
		if err != nil {
			stats.Error = fmt.Errorf("create failed for %s: %s",
				remoteFile, err)
			return stats
		}
		defer fr.Close()

		fl, err := os.Open(localFile)
		if err != nil {
			stats.Error = err
			return stats
		}
		defer fl.Close()
		fSize, err := fl.Stat()
		if err != nil {
			stats.Error = err
			return stats
		}

		chunkSize := SingleMB
		var written int64
		stats := UpdateStats{}
		stats.Size = fSize.Size()
		for {
			if written, err = io.CopyN(fr, fl, chunkSize); err != nil && err != io.EOF {
				stats.Error = err
				return stats
			}
			stats.Asize += written //we should process all chunks, not only divisible by chunkSize
			if prgNotify != nil {
				select {
				case prgNotify <- stats:
				default: //ignore we cannot write
				}
			}
			if written != chunkSize {
				// Must have reached EOF
				return stats
			}
		}
		// control never gets here - we will return from inside the loop.
	case "stat":
		file, err := client.Lstat(remoteFile)
		if err != nil {
			stats.Error = fmt.Errorf("lstat failed for %s: %s",
				remoteFile, err)
			stats.Error = err
			return stats
		}
		stats.ContentLength = file.Size()
		return stats
	case "rm":
		err := client.Remove(remoteFile)
		if err != nil {
			stats.Error = fmt.Errorf("remove failed for %s: %s",
				remoteFile, err)
		}
		return stats
	default:
		stats.Error = fmt.Errorf("unknown subcommand: %v", cmd)
		return stats
	}
}
