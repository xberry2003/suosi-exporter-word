package logic

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func GetLoginCookieString(loginURL string, waitCookieKey string) string {
	tempDir, err := ioutil.TempDir("", "chromedp-user-data")
	if err != nil {
		log.Fatal(err)
	}
	tempDir2, err := ioutil.TempDir("", "chromedp-disk-cache")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir2)

	procCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	execPath := FindExecPath()
	if execPath == "" {
		log.Fatal(errors.New("chrome path is not found"))
	}
	cmd := exec.CommandContext(procCtx, execPath,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-gpu",
		"--no-sandbox",
		"--user-data-dir="+tempDir,
		"--disk-cache-dir="+tempDir2,
		"--remote-debugging-port=9222",
		"--remote-debugging-address=0.0.0.0",
		`--user-agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/75.0.3770.100 Safari/537.36"`,
	)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Fatal(err)
	}
	defer stderr.Close()
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	wsURL, err := ReadOutput(stderr, nil, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(wsURL)
	ExitFunc(cmd.Process)

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), wsURL)
	defer allocCancel()
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()
	if err := chromedp.Run(taskCtx, chromedp.Navigate(loginURL)); err != nil {
		log.Fatal(errors.New("Navigate: " + err.Error()))
	}
	return WaitLoginReturnCookieString(taskCtx, waitCookieKey)
}

func WaitLoginReturnCookieString(ctx context.Context, waitCookieKey string) string {
	cookieStr := ""
	waitCookieKeyExist := false
	for {
		var cookies = []*network.Cookie{}
		if err := chromedp.Run(ctx,
			func() chromedp.ActionFunc {
				return func(ctx context.Context) error {
					cookies, _ = network.GetAllCookies().Do(ctx)
					return nil
				}
			}(),
		); err != nil {
			log.Fatal(errors.New("GetAllCookies Error: " + err.Error()))
		}
		for _, cookie := range cookies {
			cookieStr += fmt.Sprintf("%s=%s;", cookie.Name, cookie.Value)
			if cookie.Name == waitCookieKey {
				waitCookieKeyExist = true
			}
		}
		if waitCookieKeyExist {
			break
		}
		cookieStr = ""
		time.Sleep(1 * time.Second)
	}
	return cookieStr
}

