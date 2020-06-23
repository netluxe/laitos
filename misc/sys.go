package misc

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/HouzuoGuo/laitos/lalog"
	"github.com/HouzuoGuo/laitos/platform"
	"github.com/HouzuoGuo/laitos/testingstub"
)

var RegexVmRss = regexp.MustCompile(`VmRSS:\s*(\d+)\s*kB`)               // Parse VmRss value from /proc/*/status line
var RegexMemAvailable = regexp.MustCompile(`MemAvailable:\s*(\d+)\s*kB`) // Parse MemAvailable value from /proc/meminfo
var RegexMemTotal = regexp.MustCompile(`MemTotal:\s*(\d+)\s*kB`)         // Parse MemTotal value from /proc/meminfo
var RegexMemFree = regexp.MustCompile(`MemFree:\s*(\d+)\s*kB`)           // Parse MemFree value from /proc/meminfo
var RegexTotalUptimeSec = regexp.MustCompile(`(\d+).*`)                  // Parse uptime seconds from /proc/meminfo

const (
	// CommonOSCmdTimeoutSec is the number of seconds to tolerate for running a wide range of system management utilities.
	CommonOSCmdTimeoutSec = 30
)

// Use regex to parse input string, and return an integer parsed from specified capture group, or 0 if there is no match/no integer.
func FindNumInRegexGroup(numRegex *regexp.Regexp, input string, groupNum int) int {
	match := numRegex.FindStringSubmatch(input)
	if match == nil || len(match) <= groupNum {
		return 0
	}
	val, err := strconv.Atoi(match[groupNum])
	if err == nil {
		return val
	}
	return 0
}

// Return RSS memory usage of this process. Return 0 if the memory usage cannot be determined.
func GetProgramMemoryUsageKB() int {
	statusContent, err := ioutil.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	return FindNumInRegexGroup(RegexVmRss, string(statusContent), 1)
}

// Return operating system memory usage. Return 0 if the memory usage cannot be determined.
func GetSystemMemoryUsageKB() (usedKB int, totalKB int) {
	infoContent, err := ioutil.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	totalKB = FindNumInRegexGroup(RegexMemTotal, string(infoContent), 1)
	available := FindNumInRegexGroup(RegexMemAvailable, string(infoContent), 1)
	if available == 0 {
		usedKB = totalKB - FindNumInRegexGroup(RegexMemFree, string(infoContent), 1)
	} else {
		usedKB = totalKB - available
	}
	return
}

// Return system load information and number of processes from /proc/loadavg. Return empty string if IO error occurs.
func GetSystemLoad() string {
	content, err := ioutil.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

// Get system uptime in seconds. Return 0 if it cannot be determined.
func GetSystemUptimeSec() int {
	content, err := ioutil.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	return FindNumInRegexGroup(RegexTotalUptimeSec, string(content), 1)
}

/*
PrepareUtilities resets program environment PATH to be a comprehensive list of common executable locations, then
it copies non-essential laitos utility programs to a designated directory.

This is a rather expensive function due to involvement of heavy file IO, and be aware that the OS template on AWS
ElasticBeanstalk aggressively clears /tmp at regular interval, therefore caller may want to to invoke this function at
regular interval.
*/
func PrepareUtilities(progress lalog.Logger) {
	if HostIsWindows() {
		progress.Info("PrepareUtilities", "", nil, "will not do anything on Windows")
		return
	}
	progress.Info("PrepareUtilities", "", nil, "going to reset program environment PATH and copy non-essential utility programs to "+platform.UtilityDir)
	os.Setenv("PATH", platform.CommonPATH)
	if err := os.MkdirAll(platform.UtilityDir, 0755); err != nil {
		progress.Warning("PrepareUtilities", "", err, "failed to create directory %s", platform.UtilityDir)
		return
	}
	srcDestName := []string{
		"busybox-1.31.0-x86_64", "busybox",
		"busybox-x86_64", "busybox",
		"busybox", "busybox",
		"toybox-0.8.2-x86_64", "toybox",
		"toybox-x86_64", "toybox",
		"toybox", "toybox",
		"phantomjs-2.1.1-x86_64", "phantomjs",
		"phantomjs", "phantomjs",
	}
	// The GOPATH directory is useful for developing test cases, and CWD is useful for running deployed laitos.
	findInPaths := []string{filepath.Join(os.Getenv("GOPATH"), "/src/github.com/HouzuoGuo/laitos/extra/linux"), "./"}
	for i := 0; i < len(srcDestName); i += 2 {
		srcName := srcDestName[i]
		destName := srcDestName[i+1]
		for _, aPath := range findInPaths {
			srcPath := filepath.Join(aPath, srcName)
			//progress.Info("PrepareUtilities", destName, nil, "looking for %s", srcPath)
			if _, err := os.Stat(srcPath); err != nil {
				//progress.Info("PrepareUtilities", destName, err, "failed to stat srcPath %s", srcPath)
				continue
			}
			from, err := os.Open(srcPath)
			if err != nil {
				//progress.Info("PrepareUtilities", destName, err, "failed to open srcPath %s", srcPath)
				continue
			}
			defer from.Close()
			destPath := filepath.Join(platform.UtilityDir, destName)
			to, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
			if err != nil {
				//progress.Info("PrepareUtilities", destName, err, "failed to open destPath %s ", destPath)
				continue
			}
			defer to.Close()
			if err := os.Chmod(destPath, 0755); err != nil {
				//progress.Info("PrepareUtilities", destName, err, "failed to chmod %s", destPath)
				continue
			}
			if _, err = io.Copy(to, from); err == nil {
				progress.Info("PrepareUtilities", destName, err, "successfully copied from %s to %s", srcPath, destPath)
			}
		}
	}
}

// PowerShellInterpreterPath is the absolute path to PowerShell interpreter executable.
const PowerShellInterpreterPath = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`

// GetDefaultShellInterpreter returns absolute path to the default system shell interpreter. Returns "" if one cannot be found.
func GetDefaultShellInterpreter() string {
	if HostIsWindows() {
		return PowerShellInterpreterPath
	} else {
		// Find a Unix-style shell interpreter with a preference to use bash
		for _, shellName := range []string{"bash", "dash", "zsh", "ksh", "ash", "tcsh", "csh", "sh"} {
			for _, pathPrefix := range []string{"/bin", "/usr/bin", "/usr/local/bin", "/opt/bin"} {
				shellPath := filepath.Join(pathPrefix, shellName)
				if _, err := os.Stat(shellPath); err == nil {
					return shellPath
				}
			}
		}
	}
	return ""
}

/*
InvokeShell launches an external shell process with time constraints to run a piece of shell code. The code is fed into
shell command parameter "-c", which happens to be universally accepted by Unix shells and Windows Powershell.
Returns shell stdout+stderr output combined and error if there is any. The maximum acount of output is capped to
MaxExternalProgramOutputBytes.
*/
func InvokeShell(timeoutSec int, interpreter string, content string) (out string, err error) {
	return platform.InvokeProgram(nil, timeoutSec, interpreter, "-c", content)
}

// GetSysctlStr returns string value of a sysctl parameter corresponding to the input key.
func GetSysctlStr(key string) (string, error) {
	content, err := ioutil.ReadFile(filepath.Join("/proc/sys/", strings.Replace(key, ".", "/", -1)))
	return strings.TrimSpace(string(content)), err
}

// GetSysctlInt return integer value of a sysctl parameter corresponding to the input key.
func GetSysctlInt(key string) (int, error) {
	val, err := GetSysctlStr(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(val)
}

// SetSysctl writes a new value into sysctl parameter.
func SetSysctl(key, value string) (old string, err error) {
	filePath := filepath.Join("/proc/sys/", strings.Replace(key, ".", "/", -1))
	if old, err = GetSysctlStr(key); err != nil {
		return
	}
	err = ioutil.WriteFile(filePath, []byte(strings.TrimSpace(value)), 0644)
	return
}

// IncreaseSysctlInt increases sysctl parameter to the specified value. If value already exceeds, it is left untouched.
func IncreaseSysctlInt(key string, atLeast int) (old int, err error) {
	old, err = GetSysctlInt(key)
	if err != nil {
		return
	}
	if old < atLeast {
		_, err = SetSysctl(key, strconv.Itoa(atLeast))
	}
	return
}

// HostIsCircleCI returns true only if the host environment is on Circle CI.
func HostIsCircleCI() bool {
	return os.Getenv("CIRCLECI") != ""
}

// SkipTestIfCI asks a test to be skipped if it is being run on Circle CI.
func SkipTestIfCI(t testingstub.T) {
	if os.Getenv("CIRCLECI") != "" {
		t.Skip("this test is skipped on CircleCI")
	}
}

// HostIsWSL returns true only if the runtime is Windows subsystem for Linux.
func HostIsWSL() bool {
	cmd := exec.Command("uname", "-a")
	out, err := cmd.CombinedOutput()
	return err == nil && strings.Contains(string(out), "Microsoft")
}

// SkipIfWSL asks a test to be skipped if it is being run on Windows Subsystem For Linux.
func SkipIfWSL(t testingstub.T) {
	if HostIsWSL() {
		t.Skip("this test is skipped on Windows Subsystem For Linux")
	}
}

// HostIsWindows returns true only if the runtime is Windows native. It returns false in other cases, including Windows Subsystem For Linux.
func HostIsWindows() bool {
	return runtime.GOOS == "windows"
}

// SkipIfWindows asks a test to be skipped if it is being run on Windows natively (not "subsystem for Linux").
func SkipIfWindows(t testingstub.T) {
	if HostIsWindows() {
		t.Skip("this test is skipped on Windows")
	}
}

/*
GetLocalUserNames returns all user names from /etc/passwd (Unix-like) or local account names (Windows). It returns an
empty map if the names cannot be retrieved.
*/
func GetLocalUserNames() (ret map[string]bool) {
	ret = make(map[string]bool)
	if HostIsWindows() {
		out, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\System32\Wbem\WMIC.exe`, "useraccount", "get", "name")
		if err != nil {
			return
		}
		for _, name := range strings.Split(out, "\n") {
			name = strings.TrimSpace(name)
			// Skip trailing empty line and Name header line
			if name == "" || strings.ToLower(name) == "name" {
				continue
			}
			ret[name] = true
		}
	} else {
		passwd, err := ioutil.ReadFile("/etc/passwd")
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(passwd), "\n") {
			idx := strings.IndexRune(line, ':')
			if idx > 0 {
				ret[line[:idx]] = true
			}
		}
	}
	return
}

// BlockUserLogin uses many independent mechanisms to stop a user from logging into system.
func BlockUserLogin(userName string) (ok bool, out string) {
	ok = true
	if HostIsWindows() {
		progOut, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\net.exe`, "user", userName, "/active:no")
		if err != nil {
			ok = false
			out += fmt.Sprintf("net user failed: %v - %s\n", err, strings.TrimSpace(progOut))
		}
	} else {
		// Some systems use chsh while some others use chmod
		progOut, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "chsh", "-s", "/bin/false", userName)
		if err != nil {
			usermodOut, usermodErr := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "usermod", "-s", "/bin/false", userName)
			if usermodErr != nil {
				ok = false
				out += fmt.Sprintf("chsh failed (%v - %s) and then usermod shell failed as well: %v - %s\n", err, strings.TrimSpace(progOut), usermodErr, strings.TrimSpace(usermodOut))
			}
		}
		progOut, err = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "passwd", "-l", userName)
		if err != nil {
			ok = false
			out += fmt.Sprintf("passwd failed: %v - %s\n", err, strings.TrimSpace(progOut))
		}
		progOut, err = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "usermod", "--expiredate", "1", userName)
		if err != nil {
			ok = false
			out += fmt.Sprintf("usermod expiry failed: %v - %s\n", err, strings.TrimSpace(progOut))
		}
	}
	return
}

// DisableStopDaemon disables a system service and prevent it from ever starting again.
func DisableStopDaemon(daemonNameNoSuffix string) (ok bool) {
	if HostIsWindows() {
		// "net stop" conveniently stops dependencies as well
		if out, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\net.exe`, "stop", "/yes", daemonNameNoSuffix); err == nil || strings.Contains(strings.ToLower(out), "is not started") {
			ok = true
		}
		/*
			Be aware that, if "sc stop" responds with:
			"The specified service does not exist as an installed service."
			The response is actually saying there is denied access and it cannot determine the state of the service.
		*/
		if out, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\sc.exe`, "stop", daemonNameNoSuffix); err == nil || strings.Contains(strings.ToLower(out), "has not been started") {
			ok = true
		}
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\sc.exe`, "config", daemonNameNoSuffix, "start=", "disabled"); err == nil {
			ok = true
		}
	} else {
		// Some hosting providers still have not used systemd yet, such as the OS on Elastic Beanstalk.
		_, _ = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "/etc/init.d/"+daemonNameNoSuffix, "stop")
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "chkconfig", " --level", "0123456", daemonNameNoSuffix, "off"); err == nil {
			ok = true
		}
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "chmod", "0000", "/etc/init.d/"+daemonNameNoSuffix); err == nil {
			ok = true
		}
		_, _ = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "stop", daemonNameNoSuffix+".service")
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "disable", daemonNameNoSuffix+".service"); err == nil {
			ok = true
		}
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "mask", daemonNameNoSuffix+".service"); err == nil {
			ok = true
		}
	}
	return
}

// EnableStartDaemon enables and starts a system service.
func EnableStartDaemon(daemonNameNoSuffix string) (ok bool) {
	if HostIsWindows() {
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\sc.exe`, "config", daemonNameNoSuffix, "start=", "auto"); err == nil {
			ok = true
		}
		if out, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, `C:\Windows\system32\sc.exe`, "start", daemonNameNoSuffix); err == nil || strings.Contains(strings.ToLower(out), "already running") {
			ok = true
		}
	} else {
		// Some hosting providers still have not used systemd yet, such as the OS on Elastic Beanstalk.
		_, _ = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "chmod", "0755", "/etc/init.d/"+daemonNameNoSuffix)
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "chkconfig", " --level", "345", daemonNameNoSuffix, "on"); err == nil {
			ok = true
		}
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "/etc/init.d/"+daemonNameNoSuffix, "start"); err == nil {
			ok = true
		}
		_, _ = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "unmask", daemonNameNoSuffix+".service")
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "enable", daemonNameNoSuffix+".service"); err == nil {
			ok = true
		}
		if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "start", daemonNameNoSuffix+".service"); err == nil {
			ok = true
		}
	}
	return
}

/*
DisableInterferingResolved disables systemd-resolved service to prevent it from interfering with laitos DNS server daemon.
Otherwise, systemd-resolved daemon listens on 127.0.0.53:53 and prevents laitos DNS server from listening on all network interfaces (0.0.0.0).
*/
func DisableInterferingResolved() (out string) {
	if _, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "is-active", "systemd-resolved"); err != nil {
		return "will not change name resolution settings as systemd-resolved is not active"
	}
	// Read the configuration file, it may have already been overwritten by systemd-resolved.
	originalContent, err := ioutil.ReadFile("/etc/resolv.conf")
	var hasUplinkNameServer bool
	if err == nil {
		for _, line := range strings.Split(string(originalContent), "\n") {
			if regexp.MustCompile(`^\s*nameserver.+$`).MatchString(line) && !regexp.MustCompile(`^\s*nameserver\s+127\..*$`).MatchString(line) {
				hasUplinkNameServer = true
				break
			}
		}
	}
	// Stop systemd-resolved but do not disable it, it still helps to collect uplink DNS server configuration next boot.
	_, err = platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "systemctl", "stop", "systemd-resolved.service")
	if err != nil {
		out += "failed to stop systemd-resolved.service\n"
	}
	// Distributions that use systemd-resolved usually makes resolv.conf a symbol link to an automatically generated file
	os.RemoveAll("/etc/resolv.conf")
	var newContent string
	if hasUplinkNameServer {
		// The configuration created by systemd-resolved connects directly to uplink DNS servers (e.g. LAN), hence retaining the configuration.
		out += "retaining uplink DNS server configuration\n"
		newContent = string(originalContent)
	} else {
		/*
			Create a new resolv.conf consisting of primary servers of popular public DNS resolvers.
			glibc cannot use more than three DNS resolvers.
		*/
		out += "using public DNS servers\n"
		newContent = `
# Generated by laitos software - DisableInterferingResolved
options rotate timeout:3 attempts:3
# Quad9, OpenDNS, AdGuard primary
nameserver 9.9.9.9
nameserver 208.67.222.222
nameserver 176.103.130.130
`
	}
	if err := ioutil.WriteFile("/etc/resolv.conf", []byte(newContent), 0644); err == nil {
		out += "resolv.conf has been reset\n"
	} else {
		out += fmt.Sprintf("failed to overwrite resolv.conf - %v\n", err)
	}
	return
}

// SwapOff turns off all swap files and partitions for improved system confidentiality.
func SwapOff() error {
	// Wait quite a while to ensure that caller gets an accurate result return value.
	out, err := platform.InvokeProgram(nil, CommonOSCmdTimeoutSec, "swapoff", "-a")
	if err != nil {
		return fmt.Errorf("SwapOff: %v - %s", err, out)
	}
	return nil
}

// SetTimeZone changes system time zone to the specified value (such as "UTC").
func SetTimeZone(zone string) error {
	zoneInfoPath := filepath.Join("/usr/share/zoneinfo/", zone)
	if stat, err := os.Stat(zoneInfoPath); err != nil || stat.IsDir() {
		return fmt.Errorf("failed to read zoneinfo file of %s - %v", zone, err)
	}
	os.Remove("/etc/localtime")
	if err := os.Symlink(zoneInfoPath, "/etc/localtime"); err != nil {
		return fmt.Errorf("failed to make localtime symlink: %v", err)
	}
	return nil
}
