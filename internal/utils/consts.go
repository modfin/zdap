package utils

import "regexp"

const TimestampFormat = "2006-01-02T15:04:05Z07"

var TimestampFormatRegexp = regexp.MustCompile("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(Z)$")
