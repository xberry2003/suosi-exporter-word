package logic

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func FindExecPath() string {
	candidates := []string{
		"headless_shell",
		"headless-shell",
		"chromium",
		"chromium-browser",
		"google-chrome",
		"google-chrome-stable",
		"google-chrome-beta",
		"google-chrome-unstable",
		"/usr/bin/google-chrome",
		"chrome",
		"chrome.exe",
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	}
	for _, path := range candidates {
		found, err := exec.LookPath(path)
		if err == nil {
			return found
		}
	}
	return ""
}

func ReadOutput(rc io.ReadCloser, forward io.Writer, done func()) (wsURL string, _ error) {
	prefix := []byte("DevTools listening on")
	var accumulated bytes.Buffer
	bufr := bufio.NewReader(rc)
	for {
		line, err := bufr.ReadBytes('\n')
		if err != nil {
			return "", fmt.Errorf("chrome failed to start:\n%s", accumulated.Bytes())
		}
		if forward != nil {
			if _, err := forward.Write(line); err != nil {
				return "", err
			}
		}
		if bytes.HasPrefix(line, prefix) {
			line = line[len(prefix):]
			line = bytes.TrimSpace(line)
			wsURL = string(line)
			break
		}
		accumulated.Write(line)
	}
	if forward == nil {
		_ = rc.Close()
	} else {
		go func() {
			_, _ = io.Copy(forward, bufr)
			if done != nil {
				done()
			}
		}()
	}
	return wsURL, nil
}

func ExitFunc(process *os.Process) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		for s := range c {
			switch s {
			case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT:
				log.Println("exit", s)
				_ = process.Kill()
				os.Exit(0)
			}
		}
	}()
}

func ClearDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}

func GetCurrentDirectory() string {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		log.Fatal(err)
	}
	return strings.ReplaceAll(dir, "\\", "/")
}

func Exec(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return output.String(), nil
}
