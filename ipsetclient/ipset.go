package main

import (
	"bytes"
	"github.com/bitly/nsq/nsq"
	"log"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (this *IPSet) setup() {
	this.setup_hashset()
	this.setup_iphash()
}

func (this *IPSet) setup_hashset() error {
	_, err := exec.Command("/usr/bin/sudo",
		"/usr/sbin/ipset", "-L", this.HashSetName).Output()
	if err == nil {
		log.Println("setlist ", this.HashSetName, " exist!")
		return nil
	}
	cmd := exec.Command("/usr/bin/sudo",
		"/usr/sbin/ipset", "-N", this.HashSetName, "setlist")
	if err = cmd.Run(); err != nil {
		log.Fatal("ipset create setlist failed:", err)
	}
	_, err = exec.Command("/usr/bin/sudo",
		"/usr/sbin/ipset", "-F",
		this.HashSetName).Output()
	return err
}

func (this *IPSet) setup_iphash() {
	this.HashList = this.HashList[:0]
	for this.index = 0; this.index < this.maxsize; this.index++ {
		name := this.HashName + strconv.Itoa(this.index)
		this.HashList = append(this.HashList, name)
		_, err := exec.Command("/usr/bin/sudo",
			"/usr/sbin/ipset", "-L",
			name).Output()
		if err == nil {
			log.Println("iphash ",
				this.HashList[this.index], " exist!")
		} else {
			cmd := exec.Command("/usr/bin/sudo",
				"/usr/sbin/ipset", "-N",
				name, "iphash")
			if err := cmd.Run(); err != nil {
				log.Println("ipset create iphash ",
					name, " failed:", err)
			}
		}
		this.add_hashset(name)
	}
	this.index = 0
}

func (this *IPSet) add_hashset(name string) {
	_, err := exec.Command("/usr/bin/sudo",
		"/usr/sbin/ipset", "-D", this.HashSetName, name).Output()
	cmd := exec.Command("/usr/bin/sudo",
		"/usr/sbin/ipset", "-A", this.HashSetName, name)
	if err = cmd.Run(); err != nil {
		log.Println("ipset add ", name,
			" to ", this.HashSetName, " setlist failed:", err)
	}
}

func (this *IPSet) HandleMessage(m *nsq.Message) error {
	req, e := url.ParseQuery(string(m.Body))
	if e != nil {
		log.Println("bad req", string(m.Body), e)
		return nil
	}
	var action string
	if len(req["action_type"]) > 0 {
		action = req["action_type"][0]
	}
	var ipaddresses []string
	if len(req["ip"]) > 0 {
		ips := req["ip"]
		for _, v := range ips {
			items := strings.Split(v, ",")
			ipaddresses = append(ipaddresses, items...)
		}
	}
	var timeout int
	if len(req["timeout"]) > 0 {
		timeout, _ = strconv.Atoi(req["timeout"][0])
	}
	switch action {
	case "add":
		go this.update_ip(ipaddresses, timeout)
		log.Println("add", ipaddresses, timeout)
	case "del":
		go this.del_ip(ipaddresses)
		log.Println("del", ipaddresses)
	case "clear":
		go this.clear_ip()
	case "update":
		go this.update_ip(ipaddresses, timeout)
		log.Println("update", ipaddresses)
	case "stop":
		go this.stop_expire(timeout)
	default:
		log.Println("ignore action:", action)
	}
	return nil
}

func (this *IPSet) del_ip(ipaddresses []string) {
	for _, ip := range ipaddresses {
		for _, h := range this.HashList {
			exec.Command("/usr/bin/sudo", "/usr/sbin/ipset", "-D",
				h, ip).Output()
		}
		this.iplock.Lock()
		if _, ok := this.iplist[ip]; ok {
			this.iplist[ip].Stop()
			delete(this.iplist, ip)
		}
		this.iplock.Unlock()
	}
}
func (this *IPSet) clear_ip() {
	for _, h := range this.HashList {
		exec.Command("/usr/bin/sudo", "/usr/sbin/ipset", "-F", h).Output()
	}
	this.iplock.Lock()
	for k, _ := range this.iplist {
		delete(this.iplist, k)
	}
	this.iplock.Unlock()
}
func (this *IPSet) update_ip(ipaddresses []string, timeout int) {
	for _, ip := range ipaddresses {
		if len(ip) < 7 {
			return
		}
		this.iplock.Lock()
		_, ok := this.iplist[ip]
		this.iplock.Unlock()
		if ok {
			return
		}
		this.iplock.Lock()
		this.iplist[ip] = time.AfterFunc(
			time.Duration(timeout)*time.Second,
			func() { this.expireChan <- ip })
		this.iplock.Unlock()
		cmd := exec.Command("/usr/bin/sudo", "/usr/sbin/ipset",
			"-A", this.HashList[this.index], ip)
		var output bytes.Buffer
		cmd.Stderr = &output
		err := cmd.Run()
		if err != nil {
			reg, e := regexp.Compile("set is full")
			if e == nil && reg.MatchString(output.String()) {
				log.Printf("ipset %s is full",
					this.HashList[this.index])
				this.Lock()
				if this.index < this.maxsize {
					this.index++
				} else {
					this.index = 0
				}
				this.Unlock()
				this.update_ip([]string{ip}, timeout)
			} else {
				this.iplock.Lock()
				delete(this.iplist, ip)
				this.iplock.Unlock()
			}
		}
	}
}
func (this *IPSet) stop_expire(timeout int) {
	log.Println("stop auto expire", timeout)
	this.sleepChan <- timeout
}
func (this *IPSet) expire() {
	for {
		select {
		case ip := <-this.expireChan:
			this.del_ip([]string{ip})
		case i := <-this.sleepChan:
			time.Sleep(time.Second * time.Duration(i))
		}
	}
}
