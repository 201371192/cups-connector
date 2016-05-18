// Copyright 2015 Google Inc. All rights reserved.

// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd

// +build windows

// The log package logs to the Windows Event Log, or stdout.
package log

import (
	"fmt"
	"log"
	"os"
	
	"github.com/google/cups-connector/lib"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
)

const (
	logJobFormat     = "[Job %s] %s"
	logPrinterFormat = "[Printer %s] %s"

	dateTimeFormat = "2006-Jan-02 15:04:05"
)

var logger struct {
	level LogLevel
	elog  debug.Log
	filePath string
	
}

func init() {
	logger.level = INFO
		d,_:=os.Open(os.Getenv("appdata")+"\\Princh")
	fi, _ := d.Readdir(-1)
	 if(logger.filePath==""){
		 StringExistInFolder:= StringInFileInfo(fi,"PrinchConnectorLog")
		if(StringExistInFolder!=""){
			fmt.Println(StringExistInFolder)
			logger.filePath=StringExistInFolder
		}	 
		 
		 
		 
	 }
	
}

// SetLevel sets the minimum severity level to log. Default is INFO.
func SetLevel(l LogLevel) {
	logger.level = l
}
func SetPath(filePath string){
	logger.filePath= filePath
}


func Start(logToConsole bool) error {
	if logToConsole {
		logger.elog = debug.New(lib.ConnectorName)

	} else {
		l, err := eventlog.Open(lib.ConnectorName)
		if err != nil {
			return err
		}
		logger.elog = l
	}

	return nil
}

func Stop() {
	err := logger.elog.Close()
	if err != nil {
		panic("Failed to close log")
	}
}

func logToEventlog(level LogLevel, printerID, jobID, format string, args ...interface{}) {
	if logger.elog == nil {
		panic("Attempted to log without first calling Start()")
	}

	if level > logger.level {
		return
	}

	var message string
	if format == "" {
		message = fmt.Sprint(args...)
	} else {
		message = fmt.Sprintf(format, args...)
	}

	if printerID != "" {
		message = fmt.Sprintf(logPrinterFormat, printerID, message)
	} else if jobID != "" {
		message = fmt.Sprintf(logJobFormat, jobID, message)
	}

	if level == DEBUG || level == FATAL {
		// Windows Event Log only has three levels; these two extra information prepended.
		message = fmt.Sprintf("%s %s", stringByLevel[level], message)
	}

	switch level {
	case FATAL, ERROR:
		logger.elog.Error(1, message)
		logToFile(message)
	case WARNING:
		logger.elog.Warning(2, message)
		logToFile(message)
	case INFO, DEBUG:
		logger.elog.Info(3, message)
		logToFile(message)
	}
}
func logToFile(message string){
	
	f, err := os.OpenFile(logger.filePath, os.O_RDWR | os.O_CREATE | os.O_APPEND, 0666)
		if err != nil {
 		  fmt.Println("failed to open file")
		}
		defer f.Close()
		log.SetOutput(f)
		log.Println(message)
		err=f.Close()
		if err != nil {
 		fmt.Println("failed to close file")
		}
		
}
