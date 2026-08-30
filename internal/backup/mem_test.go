package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// benchmarkPayloadSizes are the approximate SQLite snapshot sizes the memory
// benchmarks sweep. Reporting several sizes is the point: it distinguishes a
// peak that tracks the archive from one that is a constant (the scrypt working
// set of 128*N*r == 32 MiB at the default parameters, plus a frame buffer).
var benchmarkPayloadSizes = []int{16 << 20, 64 << 20, 112 << 20}

// peakHeap runs fn while sampling runtime.MemStats and returns the largest
// observed HeapAlloc together with the number of bytes allocated in total.
//
// Sampling with the collector enabled measures the live+uncollected heap, which
// is the number that matters for "does this stall or OOM a laptop". It is a
// lower bound: a short-lived spike between two samples can be missed. TotalAlloc
// is reported alongside it as the churn upper bound.
func peakHeap(fn func()) (peak uint64, churn uint64) {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	stop := make(chan struct{})
	result := make(chan uint64, 1)
	go func() {
		var observed uint64
		var stats runtime.MemStats
		ticker := time.NewTicker(500 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > observed {
					observed = stats.HeapAlloc
				}
				result <- observed
				return
			case <-ticker.C:
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > observed {
					observed = stats.HeapAlloc
				}
			}
		}
	}()

	fn()

	close(stop)
	peak = <-result
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if peak > base.HeapAlloc {
		peak -= base.HeapAlloc
	}
	return peak, after.TotalAlloc - base.TotalAlloc
}

func benchmarkDatabase(b *testing.B, approximateBytes int) (*sql.DB, string) {
	b.Helper()
	path := filepath.Join(b.TempDir(), "active.sqlite3")
	database, err := sql.Open("sqlite", (&urlPath{path}).String())
	if err != nil {
		b.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		b.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB NOT NULL)"); err != nil {
		b.Fatal(err)
	}
	const chunk = 1 << 20
	buffer := make([]byte, chunk)
	for written := 0; written < approximateBytes; written += chunk {
		if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
			b.Fatal(err)
		}
		if _, err := database.Exec("INSERT INTO payload(body) VALUES (?)", buffer); err != nil {
			b.Fatal(err)
		}
	}
	return database, path
}

// urlPath keeps the benchmark file independent of helpers in other test files.
type urlPath struct{ path string }

func (u *urlPath) String() string { return "file:" + u.path }

// BenchmarkExportPeakMemory reports the peak heap of a single Export, and that
// peak as a multiple of the archive it produced.
func BenchmarkExportPeakMemory(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			database, _ := benchmarkDatabase(b, size)
			defer database.Close()

			var peak, churn, archiveSize uint64
			for i := 0; i < b.N; i++ {
				destination := filepath.Join(b.TempDir(), fmt.Sprintf("backup-%d.ybk", i))
				p, c := peakHeap(func() {
					if _, err := Export(context.Background(), database, destination, "correct horse battery staple", ExportOptions{}); err != nil {
						b.Fatal(err)
					}
				})
				info, err := os.Stat(destination)
				if err != nil {
					b.Fatal(err)
				}
				peak, churn, archiveSize = p, c, uint64(info.Size())
				os.Remove(destination)
			}
			reportPeak(b, peak, churn, archiveSize)
		})
	}
}

// BenchmarkRestorePeakMemory reports the same figures for Restore.
func BenchmarkRestorePeakMemory(b *testing.B) {
	for _, size := range benchmarkPayloadSizes {
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			database, _ := benchmarkDatabase(b, size)
			defer database.Close()
			archive := filepath.Join(b.TempDir(), "backup.ybk")
			if _, err := Export(context.Background(), database, archive, "correct horse battery staple", ExportOptions{}); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(archive)
			if err != nil {
				b.Fatal(err)
			}
			archiveSize := uint64(info.Size())

			var peak, churn uint64
			for i := 0; i < b.N; i++ {
				target := filepath.Join(b.TempDir(), fmt.Sprintf("restore-%d", i))
				p, c := peakHeap(func() {
					if _, err := RestoreToTemp(context.Background(), archive, "correct horse battery staple", target, RestoreOptions{}); err != nil {
						b.Fatal(err)
					}
				})
				peak, churn = p, c
				os.RemoveAll(target)
			}
			reportPeak(b, peak, churn, archiveSize)
		})
	}
}

func reportPeak(b *testing.B, peak, churn, archiveSize uint64) {
	b.Helper()
	b.ReportMetric(float64(peak)/(1<<20), "peakMiB")
	b.ReportMetric(float64(churn)/(1<<20), "churnMiB")
	b.ReportMetric(float64(archiveSize)/(1<<20), "archiveMiB")
	b.ReportMetric(float64(peak)/float64(archiveSize), "peak/archive")
}
