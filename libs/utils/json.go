package utils

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func ToJson(v interface{}) ([]byte, error) { return json.Marshal(v) }

func JsonpCallback() string {
	var cstZone = time.FixedZone("CST", 8*3600)
	t := time.Now()
	return "jQuery" + t.In(cstZone).Format("20060102150405")
}

func JSONToStruct(s string, v interface{}) error { return json.Unmarshal([]byte(s), v) }

func JSONPToStruct(s string, v interface{}) error {
	pos1 := strings.Index(s, "(")
	pos2 := strings.LastIndex(s, ")")
	if pos1 >= 0 && pos2 >= 0 {
		return JSONToStruct(s[pos1+1:pos2], v)
	}
	return errors.New("input is not valid jsonp string")
}

