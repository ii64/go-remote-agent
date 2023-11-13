package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
)

const endpointFilesystem = "/filesystem"

const textCopyrightElement = ``

var appSalt = "BUZZINGA"
var httpListenAddr = ":9080"
var partitions []disk.PartitionStat

// device -> http.FileServer
var registeredFileServer = map[string]http.Handler{}

var mux = http.NewServeMux()

func periodicCheckPartitions() {
	var err error
	var (
		newPartitions []disk.PartitionStat
		diffParts     []disk.PartitionStat
	)
	for {
		newPartitions, err = disk.Partitions(true)
		if err != nil {
			slog.Error("periodic part check", slog.Any("err", err))
			goto last
		}
		for _, part := range newPartitions {
			key := normalizePartID(part.Device, part.Mountpoint)
			if _, exist := registeredFileServer[key]; exist {
				continue
			}
			fsPrefix := fmt.Sprintf("%s/%s", endpointFilesystem, key)
			fsHandler := http.StripPrefix(fsPrefix, http.FileServer(http.Dir(part.Mountpoint)))
			registeredFileServer[key] = fsHandler
			mux.Handle(fsPrefix+"/", &nocacheServer{fsHandler})
		}
		partitions = newPartitions
		slog.Debug("periodic part check", slog.Any("parts", partitions))
		diffParts = partitionDiff(partitions, newPartitions)
		slog.Debug("partition update", slog.Any("diff", diffParts))
	last:
		time.Sleep(time.Second * 10)
	}
}

func partitionDiff(oldParts, newParts []disk.PartitionStat) (diffParts []disk.PartitionStat) {
	var hcheck = map[string]disk.PartitionStat{}
	for _, oPart := range oldParts {
		hcheck[oPart.Mountpoint] = oPart
	}
	for _, nPart := range newParts {
		if oPart, exist := hcheck[nPart.Mountpoint]; !exist {
			_ = oPart
			diffParts = append(diffParts, nPart)
		}
	}

	return
}

func normalizePartID(deviceName, mountpoint string) string {
	h := hmac.New(sha256.New, []byte(appSalt))
	h.Write([]byte(deviceName))
	h.Write([]byte(mountpoint))
	return hex.EncodeToString(h.Sum(nil))
}

func init() {
	flag.StringVar(&appSalt, "salt", appSalt, "")
	flag.StringVar(&httpListenAddr, "http-addr", httpListenAddr, "")
}

func jsonEncode(v any) []byte {
	bb, _ := json.Marshal(v)
	return bb
}

func main() {
	flag.Parse()

	var sig = make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	slog.Info("starting")
	defer func() {
		slog.Info("exit")
	}()

	mux.HandleFunc("/parts", func(w http.ResponseWriter, r *http.Request) {
		wr := bufio.NewWriter(w)
		wr.WriteString("<h1>Partitions</h1></ br>")
		wr.WriteString(`<a href="/">Back</a><br /><br />`)

		for _, part := range partitions {
			partPath := fmt.Sprintf("%s/%s/", endpointFilesystem, normalizePartID(part.Device, part.Mountpoint))
			partName := fmt.Sprintf("%s -> %s", part.Device, part.Mountpoint)
			fmt.Fprintf(wr, `<a href="%s">%s</a><pre>%s</pre><br />`, partPath, partName, jsonEncode([]any{part.Fstype, part.Opts}))
		}
		wr.WriteString(textCopyrightElement)
		wr.Flush()
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "404: page not found")
			return
		}
		fmt.Fprintf(w, `<a href="/parts">Partitions</a><br />`)
		fmt.Fprintf(w, textCopyrightElement)
	})

	go periodicCheckPartitions()
	go func() {
		slog.Info("http srv", slog.Any("addr", httpListenAddr))
		if err := http.ListenAndServe(httpListenAddr, mux); err != nil {
			slog.Error("http srv", slog.Any("err", err))
		}
	}()

	<-sig
}

type nocacheServer struct {
	h http.Handler
}

var _ http.Handler = (*nocacheServer)(nil)

func (s *nocacheServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Surrogate-Control", "no-cache")
	if s.h != nil {
		s.h.ServeHTTP(w, r)
	}
}
