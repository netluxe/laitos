// +build darwin linux

package platform

import (
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/HouzuoGuo/laitos/lalog"
)

// GetRootDiskUsageKB returns used and total space of the file system mounted on /. Returns 0 if they cannot be determined.
func GetRootDiskUsageKB() (usedKB, freeKB, totalKB int) {
	fs := syscall.Statfs_t{}
	err := syscall.Statfs("/", &fs)
	if err != nil {
		return
	}
	totalKB = int(int64(fs.Blocks) * int64(fs.Bsize) / 1024)
	freeKB = int(int64(fs.Bfree) * int64(fs.Bsize) / 1024)
	usedKB = totalKB - freeKB
	return
}

/*
InvokeProgram launches an external program with time constraints. The external program inherits laitos' environment
mixed with additional input environment variables. The additional variables take precedence over inherited ones.
Returns stdout+stderr output combined, and error if there is any. The maximum amount of output returned is capped to
MaxExternalProgramOutputBytes.
*/
func InvokeProgram(envVars []string, timeoutSec int, program string, args ...string) (out string, err error) {
	if timeoutSec < 1 {
		return "", errors.New("invalid time limit")
	}
	// Make an environment variable array of common PATH, inherited values, and newly specified values.
	defaultOSEnv := os.Environ()
	combinedEnv := make([]string, 0, 1+len(defaultOSEnv))
	// Inherit environment variables from program environment
	combinedEnv = append(combinedEnv, defaultOSEnv...)
	/*
		Put common PATH values into the mix. Since go 1.9, when environment variables contain duplicated keys, only
		the last value of duplicated key is effective. This behaviour enables caller to override PATH if deemed
		necessary.
	*/
	combinedEnv = append(combinedEnv, "PATH="+CommonPATH)
	if envVars != nil {
		combinedEnv = append(combinedEnv, envVars...)
	}
	// Collect stdout and stderr all together in a single buffer
	outBuf := lalog.NewByteLogWriter(ioutil.Discard, MaxExternalProgramOutputBytes)
	proc := exec.Command(program, args...)
	proc.Env = combinedEnv
	proc.Stdout = outBuf
	proc.Stderr = outBuf
	// Use process group so that child processes are also killed upon time out, Windows does not require this.
	proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Monitor for time out
	var timedOut bool
	timeOutTimer := time.AfterFunc(time.Duration(timeoutSec)*time.Second, func() {
		timedOut = true
		if proc.Process != nil && !KillProcess(proc.Process) {
			logger.Warning("InvokeProgram", program, nil, "failed to kill after time limit exceeded")
		}
	})
	// Start external process
	if err = proc.Start(); err != nil {
		timeOutTimer.Stop()
		return
	}
	// If the the process may 10 minutes or longer to run, then start logging how much time the process has left every minute.
	var processQuit bool
	if timeoutSec >= 10*60 {
		go func() {
			beginningSec := time.Now().Unix()
			for {
				time.Sleep(1 * time.Minute)
				if !processQuit && !timedOut {
					spentMinutes := (time.Now().Unix() - beginningSec) / 60
					timeoutRemainingMinutes := (timeoutSec - int(time.Now().Unix()-beginningSec)) / 60
					logger.Info("InvokeProgram", program, nil, "external process %d has been running for %d minutes and will time out in %d minutes",
						proc.Process.Pid, spentMinutes, timeoutRemainingMinutes)
				} else {
					break
				}
			}
		}()
	}
	// Wait for process to finish
	err = proc.Wait()
	processQuit = true
	timeOutTimer.Stop()
	if timedOut {
		err = errors.New("time limit exceeded")
	}
	out = string(outBuf.Retrieve(false))
	return
}

// KillProcess kills the process or the group of processes associated with it.
func KillProcess(proc *os.Process) (success bool) {
	if proc == nil {
		return true
	}
	// Kill process group if it is one
	if killErr := syscall.Kill(-proc.Pid, syscall.SIGKILL); killErr == nil {
		success = true
	}
	if killErr := syscall.Kill(proc.Pid, syscall.SIGKILL); killErr == nil {
		success = true
	}
	if proc.Kill() == nil {
		success = true
	}
	_, _ = proc.Wait()
	return
}

// LockMemory locks program memory to prevent swapping, protecting sensitive user data.
func LockMemory() {
	// Lock all program memory into main memory to prevent sensitive data from leaking into swap.
	if os.Geteuid() == 0 {
		/*
			0x4 is MCL_ONFAULT, a new Linux kernel feature since 4.4. It prevents the significant virtual
			memory used by go runtime from occupying too much main memory.
			See https://github.com/golang/go/issues/28114 for more background information.
		*/
		if err := syscall.Mlockall(syscall.MCL_CURRENT | syscall.MCL_FUTURE | 0x4); err != nil {
			logger.Warning("LockMemory", "", err, "failed to lock memory")
			return
		}
		logger.Warning("LockMemory", "", nil, "program has been locked into memory for safety reasons")
	} else {
		logger.Warning("LockMemory", "", nil, "program is not running as root (UID 0) hence memory cannot be locked, your private information may leak onto disk.")
	}
}
