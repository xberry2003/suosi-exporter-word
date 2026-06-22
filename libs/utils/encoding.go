package utils

import "github.com/axgle/mahonia"

func EncodingTo(s string, from string, to string) string {
	decoder := mahonia.NewDecoder(from)
	encoder := mahonia.NewEncoder(to)
	return encoder.ConvertString(decoder.ConvertString(s))
}

func GBKToUTF(s string) string {
	return EncodingTo(s, "GBK", "UTF8")
}

