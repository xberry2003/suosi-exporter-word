package utils

import "time"

func NowTimeStringCN() string {
	var cstZone = time.FixedZone("CST", 8*3600)
	return time.Now().In(cstZone).Format("2006-01-02 15:04:05")
}

func NowTimeStringCN2() string {
	var cstZone = time.FixedZone("CST", 8*3600)
	return time.Now().In(cstZone).Format("20060102150405")
}

func NowDateStringCN() string {
	var cstZone = time.FixedZone("CST", 8*3600)
	return time.Now().In(cstZone).Format("20060102")
}

func UnixTimstampSecond() int64 { return time.Now().Unix() }

func UnixTimstampMillisecond() int64 { return time.Now().UnixNano() / 1000000 }

func UnixTimstampNanosecond() int64 { return time.Now().UnixNano() }

func ParseTimeFromString(t string) (time.Time, error) {
	var cstZone = time.FixedZone("CST", 8*3600)
	return time.ParseInLocation("2006-01-02 15:04:05", t, cstZone)
}
