// +build !windows

package system

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/qiniu/logkit/metric"
)

type Processes struct {
	execPS       func() ([]byte, error)
	readProcFile func(filename string) ([]byte, error)

	forcePS   bool
	forceProc bool
}

func (p *Processes) Name() string {
	return "processes"
}

func (p *Processes) Collect() (datas []map[string]interface{}, err error) {
	// Get an empty map of metric fields
	fields := getEmptyFields()

	// Decide if we will use 'ps' to get stats (use procfs otherwise)
	usePS := true
	if runtime.GOOS == "linux" {
		usePS = false
	}
	if p.forcePS {
		usePS = true
	} else if p.forceProc {
		usePS = false
	}

	// Gather stats from 'ps' or procfs
	if usePS {
		if err := p.gatherFromPS(fields); err != nil {
			return nil, err
		}
	} else {
		if err := p.gatherFromProc(fields); err != nil {
			return nil, err
		}
	}
	datas = append(datas, fields)
	return
}

// Gets empty fields of metrics based on the OS
func getEmptyFields() map[string]interface{} {
	fields := map[string]interface{}{
		"blocked":  int64(0),
		"zombies":  int64(0),
		"stopped":  int64(0),
		"running":  int64(0),
		"sleeping": int64(0),
		"total":    int64(0),
		"unknown":  int64(0),
	}
	switch runtime.GOOS {
	case "freebsd":
		fields["idle"] = int64(0)
		fields["wait"] = int64(0)
	case "darwin":
		fields["idle"] = int64(0)
	case "openbsd":
		fields["idle"] = int64(0)
	case "linux":
		fields["dead"] = int64(0)
		fields["paging"] = int64(0)
		fields["total_threads"] = int64(0)
	}
	return fields
}

// exec `ps` to get all process states
func (p *Processes) gatherFromPS(fields map[string]interface{}) error {
	out, err := p.execPS()
	if err != nil {
		return err
	}

	for i, status := range bytes.Fields(out) {
		if i == 0 && string(status) == "STAT" {
			// This is a header, skip it
			continue
		}
		switch status[0] {
		case 'W':
			fields["wait"] = fields["wait"].(int64) + int64(1)
		case 'U', 'D', 'L':
			// Also known as uninterruptible sleep or disk sleep
			fields["blocked"] = fields["blocked"].(int64) + int64(1)
		case 'Z':
			fields["zombies"] = fields["zombies"].(int64) + int64(1)
		case 'X':
			fields["dead"] = fields["dead"].(int64) + int64(1)
		case 'T':
			fields["stopped"] = fields["stopped"].(int64) + int64(1)
		case 'R':
			fields["running"] = fields["running"].(int64) + int64(1)
		case 'S':
			fields["sleeping"] = fields["sleeping"].(int64) + int64(1)
		case 'I':
			fields["idle"] = fields["idle"].(int64) + int64(1)
		case '?':
			fields["unknown"] = fields["unknown"].(int64) + int64(1)
		default:
			log.Printf("I! processes: Unknown state [ %s ] from ps",
				string(status[0]))
		}
		fields["total"] = fields["total"].(int64) + int64(1)
	}
	return nil
}

// get process states from /proc/(pid)/stat files
func (p *Processes) gatherFromProc(fields map[string]interface{}) error {
	filenames, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return err
	}

	for _, filename := range filenames {
		_, err := os.Stat(filename)

		data, err := p.readProcFile(filename)
		if err != nil {
			return err
		}
		if data == nil {
			continue
		}

		// Parse out data after (<cmd name>)
		i := bytes.LastIndex(data, []byte(")"))
		if i == -1 {
			continue
		}
		data = data[i+2:]

		stats := bytes.Fields(data)
		if len(stats) < 3 {
			return fmt.Errorf("Something is terribly wrong with %s", filename)
		}
		switch stats[0][0] {
		case 'R':
			fields["running"] = fields["running"].(int64) + int64(1)
		case 'S':
			fields["sleeping"] = fields["sleeping"].(int64) + int64(1)
		case 'D':
			fields["blocked"] = fields["blocked"].(int64) + int64(1)
		case 'Z':
			fields["zombies"] = fields["zombies"].(int64) + int64(1)
		case 'X':
			fields["dead"] = fields["dead"].(int64) + int64(1)
		case 'T', 't':
			fields["stopped"] = fields["stopped"].(int64) + int64(1)
		case 'W':
			fields["paging"] = fields["paging"].(int64) + int64(1)
		default:
			log.Printf("I! processes: Unknown state [ %s ] in file %s",
				string(stats[0][0]), filename)
		}
		fields["total"] = fields["total"].(int64) + int64(1)

		threads, err := strconv.Atoi(string(stats[17]))
		if err != nil {
			log.Printf("I! processes: Error parsing thread count: %s", err)
			continue
		}
		fields["total_threads"] = fields["total_threads"].(int64) + int64(threads)
	}
	return nil
}

func readProcFile(filename string) ([]byte, error) {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		// Reading from /proc/<PID> fails with ESRCH if the process has
		// been terminated between open() and read().
		if perr, ok := err.(*os.PathError); ok && perr.Err == syscall.ESRCH {
			return nil, nil
		}

		return nil, err
	}

	return data, nil
}

func execPS() ([]byte, error) {
	bin, err := exec.LookPath("ps")
	if err != nil {
		return nil, err
	}

	out, err := exec.Command(bin, "axo", "state").Output()
	if err != nil {
		return nil, err
	}

	return out, err
}

func init() {
	metric.Add("processes", func() metric.Collector {
		return &Processes{
			execPS:       execPS,
			readProcFile: readProcFile,
		}
	})
}
