package utils

import (
	"io/ioutil"
	"os"
)

func FileRead(fileName string) string {
	bytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		return ""
	}
	return string(bytes)
}

func FileAppend(fileName string, s string) {
	fd, _ := os.OpenFile(fileName, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	_, _ = fd.Write([]byte(s))
	_ = fd.Close()
}

func FileWrite(fileName string, s string) {
	fd, _ := os.OpenFile(fileName, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	_, _ = fd.Write([]byte(s))
	_ = fd.Close()
}

func FileExist(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
