package grsync

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
)

// Task is high-level API under rsync
type Task struct {
	rsync *Rsync

	state *State
	log   *Log
	mutex sync.Mutex
}

// State contains information about rsync process
type State struct {
	Remain   int     `json:"remain"`
	Total    int     `json:"total"`
	Speed    string  `json:"speed"`
	Progress float64 `json:"progress"`
}

// Log contains raw stderr and stdout outputs
type Log struct {
	Stderr string `json:"stderr"`
	Stdout string `json:"stdout"`
}

// State returns information about rsync processing task
// lock mutex to avoid accessing it while ProcessStdout is writing to it
func (t *Task) State() State {
	t.mutex.Lock()
	c := *t.state
	t.mutex.Unlock()
	return c
}

// Log return structure which contains raw stderr and stdout outputs
func (t *Task) Log() Log {
	t.mutex.Lock()
	l := Log{
		Stderr: t.log.Stderr,
		Stdout: t.log.Stdout,
	}
	t.mutex.Unlock()
	return l
}

// Run starts rsync process with options
func (t *Task) Run() error {
	stderr, err := t.rsync.StderrPipe()
	if err != nil {
		return err
	}

	stdout, err := t.rsync.StdoutPipe()
	if err != nil {
		stderr.Close()
		return err
	}

	var wg sync.WaitGroup
	go processStdout(&wg, t, stdout)
	go processStderr(&wg, t, stderr)
	wg.Add(2)

	if err = t.rsync.Start(); err != nil {
		// Close pipes to unblock goroutines
		stdout.Close()
		stderr.Close()
		wg.Wait()
		return err
	}

	wg.Wait()

	return t.rsync.Wait()
}

// NewTask returns new rsync task
func NewTask(source, destination string, rsyncOptions RsyncOptions) *Task {
	// Force set required options
	rsyncOptions.HumanReadable = true
	rsyncOptions.Partial = true
	rsyncOptions.Progress = true
	rsyncOptions.Archive = true

	return &Task{
		rsync: NewRsync(source, destination, rsyncOptions),
		state: &State{},
		log:   &Log{},
	}
}

func processStdout(wg *sync.WaitGroup, task *Task, stdout io.Reader) {
	const maxPercents = float64(100)
	const minDivider = 1

	defer wg.Done()

	progressMatcher := newMatcher(`\(.+-chk=(\d+.\d+)`)
	speedMatcher := newMatcher(`(\d+\.\d+.{2}\/s)`)

	// Extract data from strings:
	//         999,999 99%  999.99kB/s    0:00:59 (xfr#9, to-chk=999/9999)
	reader := bufio.NewReader(stdout)
	for {
		logStr, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		task.mutex.Lock()
		if progressMatcher.Match(logStr) {
			task.state.Remain, task.state.Total = getTaskProgress(progressMatcher.Extract(logStr))

			copiedCount := float64(task.state.Total - task.state.Remain)
			task.state.Progress = copiedCount / math.Max(float64(task.state.Total), float64(minDivider)) * maxPercents
		}

		if speedMatcher.Match(logStr) {
			task.state.Speed = getTaskSpeed(speedMatcher.ExtractAllStringSubmatch(logStr, 2))
		}

		task.log.Stdout += logStr + "\n"
		task.mutex.Unlock()
	}
}

func processStderr(wg *sync.WaitGroup, task *Task, stderr io.Reader) {
	defer wg.Done()

	reader := bufio.NewReader(stderr)
	for {
		logStr, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		task.mutex.Lock()
		task.log.Stderr += logStr + "\n"
		task.mutex.Unlock()
	}
}

func getTaskProgress(remTotalString string) (int, int) {
	const remTotalSeparator = "/"
	const numbersCount = 2
	const (
		indexRem = iota
		indexTotal
	)

	info := strings.Split(remTotalString, remTotalSeparator)
	if len(info) < numbersCount {
		return 0, 0
	}

	remain, _ := strconv.Atoi(info[indexRem])
	total, _ := strconv.Atoi(info[indexTotal])

	return remain, total
}

func getTaskSpeed(data [][]string) string {
	if len(data) < 2 || len(data[1]) < 2 {
		return ""
	}

	return data[1][1]
}
