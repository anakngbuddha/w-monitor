package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"Zeus/storage"
)

type readPage struct {
	Limit     int
	AfterUnix int64
	AfterID   int64
}

func parseReadPage(r *http.Request) (readPage, error) {
	page := readPage{Limit: storage.DefaultQueryLimit}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > storage.DefaultQueryLimit {
			return readPage{}, fmt.Errorf("limit must be 1..%d", storage.DefaultQueryLimit)
		}
		page.Limit = n
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursor == "" {
		return page, nil
	}
	unixStr, idStr, ok := strings.Cut(cursor, ":")
	if !ok || unixStr == "" || idStr == "" {
		return readPage{}, fmt.Errorf("cursor must be unix:id")
	}
	unix, err := strconv.ParseInt(unixStr, 10, 64)
	if err != nil {
		return readPage{}, fmt.Errorf("invalid cursor timestamp")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id < 1 {
		return readPage{}, fmt.Errorf("invalid cursor id")
	}
	page.AfterUnix, page.AfterID = unix, id
	return page, nil
}

func nextReadCursor(unix int64, id int64, count, limit int) (string, bool) {
	complete := count < limit
	if complete || id < 1 {
		return "", complete
	}
	return fmt.Sprintf("%d:%d", unix, id), false
}

const defaultServerLimit = 1000

type serverPage struct {
	Limit int
	After string
}

func parseServerPage(r *http.Request) (serverPage, error) {
	page := serverPage{Limit: defaultServerLimit}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > defaultServerLimit {
			return serverPage{}, fmt.Errorf("limit must be 1..%d", defaultServerLimit)
		}
		page.Limit = n
	}
	page.After = strings.TrimSpace(r.URL.Query().Get("cursor"))
	if strings.ContainsAny(page.After, " \t\r\n") {
		return serverPage{}, fmt.Errorf("invalid server cursor")
	}
	return page, nil
}
