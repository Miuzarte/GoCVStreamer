package main

import (
	"strconv"
)

func panicIf(err error) {
	if err != nil {
		log.Panic(err)
	}
}

const FastItoaTableLength = 50 // [0,49], [0, -49]

var (
	FastItoaTablePos = [FastItoaTableLength]string{}
	FastItoaTableNeg = [FastItoaTableLength]string{}
)

func init() {
	for i := range FastItoaTableLength {
		FastItoaTablePos[i] = strconv.Itoa(i)
		FastItoaTableNeg[i] = strconv.Itoa(-i)
	}
}

func FastItoa[T int | uint](i T) string {
	if i >= 0 {
		return FastItoaTablePos[i]
	} else {
		return FastItoaTableNeg[i]
	}
}

func FastItoa4(i1 int, u1 uint, i2 int, u2 uint) (s1, s2, s3, s4 string) {
	return FastItoa(i1), FastItoa(u1), FastItoa(i2), FastItoa(u2)
}
