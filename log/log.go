/*
Copyright 2015 Google Inc. All rights reserved.

Use of this source code is governed by a BSD-style
license that can be found in the LICENSE file or at
https://developers.google.com/open-source/licenses/bsd
*/

package log

import ("strings"
"time"
"strconv"
"os"
)


// LogLevel represents a subset of the severity levels named by CUPS.
type LogLevel uint8

const (
	FATAL LogLevel = iota
	ERROR
	WARNING
	INFO
	DEBUG
)

var (
	stringByLevel = map[LogLevel]string{
		FATAL:   "FATAL",
		ERROR:   "ERROR",
		WARNING: "WARNING",
		INFO:    "INFO",
		DEBUG:   "DEBUG",
	}
	levelByString = map[string]LogLevel{
		"FATAL":   FATAL,
		"ERROR":   ERROR,
		"WARNING": WARNING,
		"INFO":    INFO,
		"DEBUG":   DEBUG,
	}
)

func LevelFromString(level string) (LogLevel, bool) {
	v, ok := levelByString[strings.ToUpper(level)]
	if !ok {
		return 0, false
	}
	return v, true
}

func Fatal(args ...interface{})                           { logToEventlog(FATAL, "", "", "", args...)
	
 }
func Fatalf(format string, args ...interface{})           { logToEventlog(FATAL, "", "", format, args...) }
func FatalJob(jobID string, args ...interface{})          { logToEventlog(FATAL, "", jobID, "", args...) }
func FatalJobf(jobID, format string, args ...interface{}) { logToEventlog(FATAL, "", jobID, format, args...) }
func FatalPrinter(printerID string, args ...interface{})  { logToEventlog(FATAL, printerID, "", "", args...) }
func FatalPrinterf(printerID, format string, args ...interface{}) {
	logToEventlog(FATAL, printerID, "", format, args...)
}

func Error(args ...interface{})                           { logToEventlog(ERROR, "", "", "", args...) }
func Errorf(format string, args ...interface{})           { logToEventlog(ERROR, "", "", format, args...) }
func ErrorJob(jobID string, args ...interface{})          { logToEventlog(ERROR, "", jobID, "", args...) }
func ErrorJobf(jobID, format string, args ...interface{}) { logToEventlog(ERROR, "", jobID, format, args...) }
func ErrorPrinter(printerID string, args ...interface{})  { logToEventlog(ERROR, printerID, "", "", args...) }
func ErrorPrinterf(printerID, format string, args ...interface{}) {
	logToEventlog(ERROR, printerID, "", format, args...)
}

func Warning(args ...interface{})                           { logToEventlog(WARNING, "", "", "", args...) }
func Warningf(format string, args ...interface{})           { logToEventlog(WARNING, "", "", format, args...) }
func WarningJob(jobID string, args ...interface{})          { logToEventlog(WARNING, "", jobID, "", args...) }
func WarningJobf(jobID, format string, args ...interface{}) { logToEventlog(WARNING, "", jobID, format, args...) }
func WarningPrinter(printerID string, args ...interface{})  { logToEventlog(WARNING, printerID, "", "", args...) }
func WarningPrinterf(printerID, format string, args ...interface{}) {
	logToEventlog(WARNING, printerID, "", format, args...)
}

func Info(args ...interface{})                           { logToEventlog(INFO, "", "", "", args...) }
func Infof(format string, args ...interface{})           { logToEventlog(INFO, "", "", format, args...) }
func InfoJob(jobID string, args ...interface{})          { logToEventlog(INFO, "", jobID, "", args...) }
func InfoJobf(jobID, format string, args ...interface{}) { logToEventlog(INFO, "", jobID, format, args...) }
func InfoPrinter(printerID string, args ...interface{})  { logToEventlog(INFO, printerID, "", "", args...) }
func InfoPrinterf(printerID, format string, args ...interface{}) {
	logToEventlog(INFO, printerID, "", format, args...)
}

func Debug(args ...interface{})                           { logToEventlog(DEBUG, "", "", "", args...) }
func Debugf(format string, args ...interface{})           { logToEventlog(DEBUG, "", "", format, args...) }
func DebugJob(jobID string, args ...interface{})          { logToEventlog(DEBUG, "", jobID, "", args...) }
func DebugJobf(jobID, format string, args ...interface{}) { logToEventlog(DEBUG, "", jobID, format, args...) }
func DebugPrinter(printerID string, args ...interface{})  { logToEventlog(DEBUG, printerID, "", "", args...) }
func DebugPrinterf(printerID, format string, args ...interface{}) {
	logToEventlog(DEBUG, printerID, "", format, args...)
}
// parses the date from a file to time
func GetTime(StringFromFileSystem string) (time.Time){
	 		     Month_:=0
				  	loc, _ := time.LoadLocation("Europe/Berlin")
			     stringArray:=strings.Split(StringFromFileSystem," ")
				 if stringArray[1]=="January"{
						 Month_=1
				 }else if stringArray[1]=="February"{
						 Month_=2
				 }else if stringArray[1]=="March"{
						 Month_=3
			     }else if stringArray[1]=="April"{
						   Month_=4
				 }else if stringArray[1]=="May"{
						   Month_=5
			 	 }else if stringArray[1]=="June"{
						   Month_=6
				 }else if stringArray[1]=="July"{
						   Month_=7
				 }else if stringArray[1]=="August"{
						   Month_=8
				 }else if stringArray[1]=="September"{
						   Month_=9
				 }else if  stringArray[1]=="October"{
						   Month_=10
				 }else if   stringArray[1]=="November"{
						   Month_=11
				 }else if stringArray[1]=="December"{
						   Month_=12
				 }
				Year_,_ :=strconv.ParseInt(stringArray[0],10,32)
				Day_,_:=strconv.ParseInt(stringArray[2],10,32)
				TimeFromString:=time.Date(int(Year_),time.Month(Month_),int(Day_),0,0,0,0,loc)
				
				return TimeFromString
}

//find the newest log file
func StringInFileInfo( fileList []os.FileInfo, StringToBeTested string) string{
	  for _, b := range fileList {
		  
        if  strings.Contains(b.Name(),StringToBeTested){
	
           FileDate:=strings.TrimPrefix(b.Name(),"PrinchConnectorLog")
		   FileDate=strings.TrimSuffix(FileDate,".txt")
		   if(FileDate==""){
			   continue
		   }else if (FileDate!=""){
		   currentTime:=time.Now()
		   TimeFromString:=GetTime(FileDate)
		   TimeDuration:=currentTime.Sub(TimeFromString)
		  
		    if(!FindNewestLogFile(fileList, TimeDuration,StringToBeTested)){
			   return os.Getenv("appdata")+"\\Princh\\"+b.Name()
			}
		   }
		}
		  
    }
           return ""
}
		
   
// has the function of finding the newest log file currently available in %appdata%/princh
func FindNewestLogFile(FileInfo []os.FileInfo, timeDura time.Duration, StringToBeTested string) bool {
	 currentTime:=time.Now()
	myCounter := 0
	for i, b := range FileInfo {
		if(i!=0){
			myCounter++
	
		}
     	if strings.Contains(b.Name(),StringToBeTested){
				
          	   SecondaryTestString:=strings.TrimPrefix(b.Name(),"PrinchConnectorLog")
			   SecondaryTestString=strings.TrimSuffix(SecondaryTestString,".txt")
			   if(SecondaryTestString==""){
				   continue
			   }
		   		 TimeFromSecondaryTestString:=GetTime(SecondaryTestString)
				 TestIfASecondaryTimeIsNewer:=currentTime.Sub(TimeFromSecondaryTestString)
			
				timeDuraHours, timeDuraMinutes, timeDuraSeconds,timeduraNanoSeconds:=ParseDuration(timeDura.String())
				secondaryTimeHours, secondaryTimeMinutes, secondaryTimeSeconds,secondaryTimeNanoSeconds:=ParseDuration(TestIfASecondaryTimeIsNewer.String())
			
				if((secondaryTimeHours<timeDuraHours||secondaryTimeHours>timeDuraHours && secondaryTimeMinutes<timeDuraMinutes || secondaryTimeHours>timeDuraHours && secondaryTimeMinutes>timeDuraMinutes && secondaryTimeSeconds<timeDuraSeconds || secondaryTimeHours>timeDuraHours && secondaryTimeMinutes>timeDuraMinutes && secondaryTimeSeconds>timeDuraSeconds && secondaryTimeNanoSeconds<timeduraNanoSeconds)) {
					myCounter++
				}
				if(myCounter!=i){
					return true
				}
			}
			
	}
	return false
}
//ParseDuration parses duration to 4 digits example 4h3m2.5s and this function returns 4 3 2 and 5
func ParseDuration( DurationString string)(int64,int64,int64,int64){
	HoursArray:=strings.Split(DurationString,"h")
	MinutesArray:=strings.Split(HoursArray[1],"m")
	SecondsArray:=strings.Split(MinutesArray[1],".")
	NanoSecondsArray:=strings.Split(SecondsArray[1],"s")
	Hours,_:=strconv.ParseInt(HoursArray[0],10,64)
	Minutes,_:=strconv.ParseInt(MinutesArray[0],10,64)
	Seconds,_:=strconv.ParseInt(SecondsArray[0],10,64)
	NanoSeconds,_:=strconv.ParseInt(NanoSecondsArray[0],10,64)
	return Hours,Minutes,Seconds,NanoSeconds
}