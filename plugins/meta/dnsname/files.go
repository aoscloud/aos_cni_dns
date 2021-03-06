package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strings"
	"text/template"

	"github.com/containernetworking/plugins/plugins/ipam/host-local/backend/disk"
	"github.com/coreos/go-iptables/iptables"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var chainArgs = []string{"-p", "udp", "-m", "udp", "--dport", "53", "-j", "ACCEPT"}

// dnsNameLock embeds the CNI disk lock so we can hang methods from it
type dnsNameLock struct {
	lock *disk.FileLock
}

// release unlocks and closes the disk lock.
func (m *dnsNameLock) release() error {
	if err := m.lock.Unlock(); err != nil {
		return err
	}
	return m.lock.Close()
}

// acquire locks the disk lock.
func (m *dnsNameLock) acquire() error {
	return m.lock.Lock()
}

// getLock returns a dnsNameLock synchronizing the configuration directory for
// the domain.
func getLock(path string) (*dnsNameLock, error) {
	l, err := disk.NewFileLock(path)
	if err != nil {
		return nil, err
	}
	return &dnsNameLock{l}, nil
}

// checkFromDNSMasqConfFile ensures that the dnsmasq conf file for
// the network interface exists or it creates it
func checkForDNSMasqConfFile(conf dnsNameFile) error {
	if _, err := os.Stat(conf.ConfigFile); err == nil {
		// the file already exists, we can proceed
		return err
	}
	newConfig, err := generateDNSMasqConfig(conf)
	if err != nil {
		return err
	}
	// Generate the template and compile it.
	return ioutil.WriteFile(conf.ConfigFile, newConfig, 0700)
}

// addIPTablesChain adds dnsmasq iptables chain
func addIPTablesChain(interfaceName string) error {
	ip, err := iptables.New()
	if err != nil {
		return err
	}
	args := append([]string{"-i", interfaceName}, chainArgs...)
	exists, err := ip.Exists("filter", "INPUT", args...)
	if err != nil {
		return err
	}
	if !exists {
		if err := ip.Insert("filter", "INPUT", 1, args...); err != nil {
			return err
		}
	}
	return nil
}

// deleteIPTablesChain deletes dnsmasq iptables chain
func deleteIPTablesChain(interfaceName string) error {
	ip, err := iptables.New()
	if err != nil {
		return err
	}
	args := append([]string{"-i", interfaceName}, chainArgs...)
	return ip.DeleteIfExists("filter", "INPUT", args...)
}

// generateDNSMasqConfig fills out the configuration file template for the dnsmasq service
func generateDNSMasqConfig(config dnsNameFile) ([]byte, error) {
	var buf bytes.Buffer
	templ, err := template.New("dnsmasq-conf-file").Parse(dnsMasqTemplate)
	if err != nil {
		return nil, err
	}
	if err := templ.Execute(&buf, config); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// appendToFile appends a new entry to the dnsmasqs hosts file
func appendToFile(path, podname string, aliases []string, ips []*net.IPNet) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			logrus.Errorf("failed to close file %q: %v", path, err)
		}
	}()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 1 {
			for _, item := range fields[1:] {
				for _, alias := range aliases {
					if alias == item {
						return errors.Errorf("Alias %s already exists", alias)
					}
				}
				if item == podname {
					return errors.Errorf("Host %s already exists", podname)
				}
			}
		}
	}
	for _, ip := range ips {
		entry := fmt.Sprintf("%s\t%s", ip.IP.String(), podname)
		for _, alias := range aliases {
			entry += fmt.Sprintf("\t%s", alias)
		}
		entry += "\n"
		if _, err = f.WriteString(entry); err != nil {
			return err
		}
		logrus.Debugf("appended %s: %s", path, entry)
	}
	return nil
}

// removeLineFromFile removes a given entry from the dnsmasq host file
func removeFromFile(path, podname string) (bool, error) {
	var (
		keepers []string
		found   bool
	)
	shouldHUP := false
	backup := fmt.Sprintf("%s.old", path)
	if err := os.Rename(path, backup); err != nil {
		if os.IsNotExist(err) {
			return shouldHUP, nil
		}
		return shouldHUP, err
	}
	f, err := os.Open(backup)
	if err != nil {
		//	if the open fails here, we need to revert things
		renameFile(backup, path)
		return shouldHUP, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			logrus.Errorf("unable to close %q: %v", backup, err)
		}
	}()

	oldFile := bufio.NewScanner(f)
	// Iterate the old file
	for oldFile.Scan() {
		fields := strings.Fields(oldFile.Text())
		// if the IP of the entry and the given IP dont match, it should
		// go into the new file
		if len(fields) > 1 && fields[1] != podname {
			keepers = append(keepers, fmt.Sprintf("%s\n", oldFile.Text()))
			continue
		}
		found = true
	}
	if !found {
		// We never found a matching record; non-fatal
		logrus.Debugf("a record for %s was never found in %s", podname, path)
	}
	fileLength, err := writeFile(path, keepers)
	if err != nil {
		renameFile(backup, path)
		return shouldHUP, err
	}
	if fileLength > 0 {
		shouldHUP = true
	}
	if err := os.Remove(backup); err != nil {
		logrus.Errorf("unable to delete '%s': %q", backup, err)
	}
	return shouldHUP, nil
}

// renameFile renames a file to backup
func renameFile(oldpath, newpath string) {
	if renameError := os.Rename(oldpath, newpath); renameError != nil {
		logrus.Errorf("unable to restore %q to %q: %v", oldpath, newpath, renameError)
	}
}

// writeFile writes a []string to the given path and returns the number
// of lines in the file
func writeFile(path string, content []string) (int, error) {
	var counter int
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := f.Close(); err != nil {
			logrus.Errorf("unable to close %q: %v", path, err)
		}
	}()

	for _, line := range content {
		if _, err := f.WriteString(line); err != nil {
			return 0, err
		}
		counter++
	}
	return counter, nil
}
