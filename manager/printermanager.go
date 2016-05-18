/*
Copyright 2015 Google Inc. All rights reserved.

Use of this source code is governed by a BSD-style
license that can be found in the LICENSE file or at
https://developers.google.com/open-source/licenses/bsd
*/

package manager

import (
"fmt"
	"hash/adler32"
	"os"
	"reflect"
	"strings"
	"os/exec"
	"time"
    "sync"
    "io/ioutil"

    "encoding/json"
	"github.com/google/cups-connector/cdd"
	"github.com/google/cups-connector/gcp"
	"github.com/google/cups-connector/lib"
	"github.com/google/cups-connector/log"
	"github.com/google/cups-connector/privet"
	"github.com/google/cups-connector/xmpp"
	
    
)

type NativePrintSystem interface {
	GetPrinters() ([]lib.Printer, error)
	GetJobState(printerName string, jobID uint32) (*cdd.PrintJobStateDiff, error)
    GetPortName(PrinterName string) ( string, error)
    GetJobStatePJLQuery(fileName string, printerName string,portName string, jobID uint32, totalPages int)(*cdd.PrintJobStateDiff, error)
    TestPrintPjlStateCapabilities(fileName string, PrinterName string, portName string, jobID uint32, totalPages int) (*cdd.PrintJobStateDiff, error, int)
	Print(printer *lib.Printer, fileName, title, user, gcpJobID string, ticket *cdd.CloudJobTicket) (uint32, int, error)
	RemoveCachedPPD(printerName string)
	StartSpoolerService() (error)
	StopSpoolerService() (error)
	
}
type PrintersInJsonFile struct {
	PrinterBlacklist []string `json:"printer_blacklist"`
    PrintersPjl []printerPjl `json:"printer_pjl"`
}


type printerQue struct{
    portName string
    jobId string
}
type printerPjl struct {
    PjlEnabled bool `json:"pjl_enabled"`
    PjlCapable int `json:"pjl_capable"`         // 0 for not possible 1 for possible 2 to run test
    PrinterName string `json:"printer_name"`
}
// Manages state and interactions between the native print system and Google Cloud Print.
type PrinterManager struct {
	native NativePrintSystem
	gcp    *gcp.GoogleCloudPrint
	xmpp   *xmpp.XMPP
	privet *privet.Privet

	printers *lib.ConcurrentPrinterMap
	hashCodeJsonFileOld []byte
	hashCodeJsonFileNew []byte
	hashCodeConfigFileOld []byte
	printerConfigFile lib.Config
	// Job stats are numbers reported to monitoring.
	jobStatsMutex sync.Mutex
	jobsDone      uint
	jobsError     uint
    ///Jsonobject of the printers that is in cloud
    PrintersInTheJsonFile PrintersInJsonFile
	// Jobs in flight are jobs that have been received, and are not
	// finished printing yet. Key is Job ID.
	jobsInFlightMutex sync.Mutex
	jobsInFlight      map[string]struct{}
    printerQue         []printerQue
    MutexCond          sync.Cond
    pjlMutex           sync.Mutex
	nativeJobQueueSize uint
	jobFullUsername    bool
	shareScope         string
	invokeQueRemover chan int
	quit chan struct{}
	logFileNewestDate string
	
}




func NewPrinterManager(native NativePrintSystem, gcp *gcp.GoogleCloudPrint, privet *privet.Privet, printerPollInterval time.Duration, nativeJobQueueSize uint, jobFullUsername bool, shareScope string, jobs <-chan *lib.Job, xmppNotifications <-chan xmpp.PrinterNotification) (*PrinterManager, error) {
	var printers *lib.ConcurrentPrinterMap
	var queuedJobsCount map[string]uint
    var PrinchPrinterfile PrintersInJsonFile
	config := new(lib.Config)
	var err error
	hashCodeConfigFileNew,_,Path:=lib.GetConfigPrinterManager("gcp-windows-connector.config.json",[]byte(""))
  	if(reflect.DeepEqual(hashCodeConfigFileNew,[]byte(""))){
		  config,err=lib.GetConfigByString(Path)
	}else{
		config=&lib.DefaultConfig
	}
  
  
    file, err := ioutil.ReadFile(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile")
	
     if (err==nil){
      json.Unmarshal(file,&PrinchPrinterfile)
     }
	
	config,err=lib.GetConfigByString(Path)
    if (err==nil){

     }
	 if(!reflect.DeepEqual(PrinchPrinterfile.PrinterBlacklist,config.PrinterBlacklist)){
		 PrinchPrinterfile.PrinterBlacklist=config.PrinterBlacklist
	 }

	if gcp != nil {
		// Get all GCP printers.
		var gcpPrinters []lib.Printer
		gcpPrinters, queuedJobsCount, err = gcp.ListPrinters()
		if err != nil {
			return nil, err
		}
		// Organize the GCP printers into a map.
		for i := range gcpPrinters {
			gcpPrinters[i].NativeJobSemaphore = lib.NewSemaphore(nativeJobQueueSize)
		}
		printers = lib.NewConcurrentPrinterMap(gcpPrinters)
	} else {
		printers = lib.NewConcurrentPrinterMap(nil)
	}
  var m sync.Mutex
  c := sync.NewCond(&m)

	// Construct.
	pm := PrinterManager{
		native: native,
		gcp:    gcp,
		privet: privet,

		printers: printers,

		jobStatsMutex: sync.Mutex{},
		jobsDone:      0,
		jobsError:     0,
		hashCodeConfigFileOld: hashCodeConfigFileNew,
        PrintersInTheJsonFile: PrinchPrinterfile,
        pjlMutex:           sync.Mutex{},
		jobsInFlightMutex: sync.Mutex{},
		jobsInFlight:      make(map[string]struct{}),
        printerQue:            []printerQue{}, 
        MutexCond:                *c,
		nativeJobQueueSize: nativeJobQueueSize,
		jobFullUsername:    jobFullUsername,
		shareScope:         shareScope,
		invokeQueRemover: make(chan int),
		quit: make(chan struct{}),
		logFileNewestDate: "",
		
	}
	pm.HandleLogFile()
	
	// Sync once before returning, to make sure things are working.
	// Ignore privet updates this first time because Privet always starts
	// with zero printers.
	if err = pm.syncPrinters(true); err != nil {
		
	}

	// Initialize Privet printers.
	if privet != nil {
		for _, printer := range pm.printers.GetAll() {
			err := privet.AddPrinter(printer, pm.printers.GetByNativeName)
			if err != nil {
				log.WarningPrinterf(printer.Name, "Failed to register locally: %s", err)
			} else {
				log.InfoPrinterf(printer.Name, "Registered locally")
			}
		}
	}

	pm.syncPrintersPeriodically(printerPollInterval)
	pm.listenNotifications(jobs, xmppNotifications)
	
	if gcp != nil {
		for gcpPrinterID := range queuedJobsCount {
			p, _ := printers.GetByGCPID(gcpPrinterID)
			go gcp.HandleJobs(&p, func() { pm.incrementJobsProcessed(false) })
		}
		go pm.QueRemover()
	}

	return &pm, nil
}

func (pm *PrinterManager) Quit() {
	close(pm.quit)
}

func (pm *PrinterManager) syncPrintersPeriodically(interval time.Duration) {
	go func() {
		t := time.NewTimer(interval)
		defer t.Stop()

		for {
			select {
			case <-t.C:
				if err := pm.syncPrinters(false); err != nil {
					log.Error(err)
				}
				t.Reset(interval)

			case <-pm.quit:
				return
			}
		}
	}()
}
func (pm *PrinterManager) getLogFileInfo() string{
	return pm.logFileNewestDate
}

//synchronizes the printers on the system with google cloud print
func (pm *PrinterManager) syncPrinters(ignorePrivet bool) error {
	log.Info("Synchronizing printers, stand by")
	pm.hashCodeJsonFileNew = lib.ComputeMd5(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile")
    var PrintersInCloud,_,_=pm.gcp.ListPrinters()
	pm.HandleLogFile()
	// Get current snapshot of native printers.
	nativePrinters, err := pm.native.GetPrinters()

    if err != nil {
		return fmt.Errorf("Sync failed while calling GetPrinters(): %s", err)
	}
 
    if (reflect.DeepEqual(pm.hashCodeJsonFileNew, pm.hashCodeJsonFileOld)==false){
    file, err := ioutil.ReadFile(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile")
     
      json.Unmarshal(file,&pm.PrintersInTheJsonFile)
     
     if err != nil {
		return fmt.Errorf("Sync failed while calling readFile(): %s", err)
	}
    }
	hashCodeConfigFileNew,_,Path:=lib.GetConfigPrinterManager("gcp-windows-connector.config.json",[]byte(""))
  	if(reflect.DeepEqual(hashCodeConfigFileNew,pm.hashCodeConfigFileOld)){
		  config,err:=lib.GetConfigByString(Path)
		  if(err!=nil){
			  fmt.Errorf("sync failed while calling getConfigByString() %s", err )
		}else if(!reflect.DeepEqual(config.PrinterBlacklist, pm.PrintersInTheJsonFile.PrinterBlacklist)){
			pm.PrintersInTheJsonFile.PrinterBlacklist=config.PrinterBlacklist
			
		}
		pm.printerConfigFile=*config
		
	}


	// Set CapsHash on all printers.
	for i := range nativePrinters {
		h := adler32.New()
		lib.DeepHash(nativePrinters[i].Tags, h)
		nativePrinters[i].Tags["tagshash"] = fmt.Sprintf("%x", h.Sum(nil))

		h = adler32.New()
		lib.DeepHash(nativePrinters[i].Description, h)
		nativePrinters[i].CapsHash = fmt.Sprintf("%x", h.Sum(nil))
	}

	// Compare the snapshot to what we know currently.
	diffs := lib.DiffPrinters(nativePrinters, pm.printers.GetAll(), pm.PrintersInTheJsonFile.PrinterBlacklist, PrintersInCloud)
	if diffs == nil {
		log.Infof("Printers are already in sync; there are %d", len(nativePrinters))
		return nil
	}

	// Update GCP.
	ch := make(chan lib.Printer, len(diffs))
	for i := range diffs {
		go pm.applyDiff(&diffs[i], ch, ignorePrivet)
	}
	currentPrinters := make([]lib.Printer, 0, len(diffs))
	for _ = range diffs {
		p := <-ch
		if p.Name != "" && !lib.StringInSlice(p.Name, pm.PrintersInTheJsonFile.PrinterBlacklist){
			currentPrinters = append(currentPrinters, p)
		}
	}

	// Update what we know.
	pm.printers.Refresh(currentPrinters)
    pm.hashCodeJsonFileOld=pm.hashCodeJsonFileNew
     pm.pjlMutex.Lock()
    b, err := json.MarshalIndent(pm.PrintersInTheJsonFile,"","")
     pm.pjlMutex.Unlock()
     if(err!=nil){
         return fmt.Errorf("failed to marshal pm.PrintersInTheJsonFile: %s",err)
     }
   if err = ioutil.WriteFile(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile", b, 0600); err != nil {
		  return fmt.Errorf("failed to write to jsonfile: %s",err)
	}
	log.Infof("Finished synchronizing %d printers", len(currentPrinters))

	return nil
}
func printerNameInSlice(a string, list []printerPjl) bool {
    for _, b := range list {
        if b.PrinterName == a {
            return true
        }
    }
    return false
}
func (pm *PrinterManager) extendPrinterPjl(element printerPjl) []printerPjl {
   
    slice:=pm.PrintersInTheJsonFile.PrintersPjl
    n := len(slice)
    if n == cap(slice) {
        // Slice is full; must grow.
        // We double its size and add 1, so if the size is zero we still grow.
        newSlice := make([]printerPjl, len(slice), 2*len(slice)+1)
        copy(newSlice, slice)
        slice = newSlice
    }
    slice = slice[0 : n+1]
    slice[n] = element
    return slice
}
//Remove a element from slice
func  (pm *PrinterManager) detendPrinterPjl(printerName string) []printerPjl {
    for i, b := range pm.PrintersInTheJsonFile.PrintersPjl {
        if(b.PrinterName==printerName){
           printerQue := append(pm.PrintersInTheJsonFile.PrintersPjl[:i], pm.PrintersInTheJsonFile.PrintersPjl[i+1:]...)
           return printerQue 
        }
    }
    
   return pm.PrintersInTheJsonFile.PrintersPjl
}
func (pm *PrinterManager) applyDiff(diff *lib.PrinterDiff, ch chan<- lib.Printer, ignorePrivet bool) {
	switch diff.Operation {
	case lib.RegisterPrinter:
		if pm.gcp != nil {
			if err := pm.gcp.Register(&diff.Printer); err != nil {
				log.ErrorPrinterf(diff.Printer.Name, "Failed to register: %s", err)
				break
			}
			log.InfoPrinterf(diff.Printer.Name+" "+diff.Printer.GCPID, "Registered in the cloud")
            if(printerNameInSlice(diff.Printer.Name,pm.PrintersInTheJsonFile.PrintersPjl)){
                log.InfoPrinterf(diff.Printer.Name,"found in jsonfile")
            }else{
                pm.pjlMutex.Lock()
                pm.PrintersInTheJsonFile.PrintersPjl=pm.extendPrinterPjl(printerPjl{PjlEnabled:true,PjlCapable:2,PrinterName:diff.Printer.Name})
                pm.pjlMutex.Unlock()
          }

			if pm.gcp.CanShare() {
				if err := pm.gcp.Share(diff.Printer.GCPID, pm.shareScope, gcp.User, true, false); err != nil {
					log.ErrorPrinterf(diff.Printer.Name, "Failed to share: %s", err)
				} else {
					log.InfoPrinterf(diff.Printer.Name, "Shared")
				}
			}
		}

		diff.Printer.NativeJobSemaphore = lib.NewSemaphore(pm.nativeJobQueueSize)

		if pm.privet != nil && !ignorePrivet {
			err := pm.privet.AddPrinter(diff.Printer, pm.printers.GetByNativeName)
			if err != nil {
				log.WarningPrinterf(diff.Printer.Name, "Failed to register locally: %s", err)
			} else {
				log.InfoPrinterf(diff.Printer.Name, "Registered locally")
			}
		}

		ch <- diff.Printer
		return

	case lib.UpdatePrinter:
		if pm.gcp != nil {
			if err := pm.gcp.Update(diff); err != nil {
				log.ErrorPrinterf(diff.Printer.Name+" "+diff.Printer.GCPID, "Failed to update: %s", err)
			} else {
				log.InfoPrinterf(diff.Printer.Name+" "+diff.Printer.GCPID, "Updated in the cloud")
			}
		}

		if pm.privet != nil && !ignorePrivet && diff.DefaultDisplayNameChanged {
			err := pm.privet.UpdatePrinter(diff)
			if err != nil {
				log.WarningPrinterf(diff.Printer.Name, "Failed to update locally: %s", err)
			} else {
				log.InfoPrinterf(diff.Printer.Name, "Updated locally")
			}
		}

		ch <- diff.Printer
		return

	case lib.DeletePrinter:
		pm.native.RemoveCachedPPD(diff.Printer.Name)

		if pm.gcp != nil {
			if err := pm.gcp.Delete(diff.Printer.GCPID); err != nil {
				log.ErrorPrinterf(diff.Printer.Name+" "+diff.Printer.GCPID, "Failed to delete from the cloud: %s", err)
				break
			}
             pm.pjlMutex.Lock()
            pm.detendPrinterPjl(diff.Printer.Name)
             pm.pjlMutex.Unlock()
			log.InfoPrinterf(diff.Printer.Name+" "+diff.Printer.GCPID, "Deleted from the cloud")
		}

		if pm.privet != nil && !ignorePrivet {
			err := pm.privet.DeletePrinter(diff.Printer.Name)
			if err != nil {
				log.WarningPrinterf(diff.Printer.Name, "Failed to delete: %s", err)
			} else {
				log.InfoPrinterf(diff.Printer.Name, "Deleted locally")
			}
		}

	case lib.NoChangeToPrinter:
		ch <- diff.Printer
		return
	}

	ch <- lib.Printer{}
}

// listenNotifications handles the messages found on the channels.
func (pm *PrinterManager) listenNotifications(jobs <-chan *lib.Job, xmppMessages <-chan xmpp.PrinterNotification) {
	go func() {
		for {
			select {
			case <-pm.quit:
				return

			case job := <-jobs:
				log.DebugJobf(job.JobID, "Received job: %+v", job)
                pm.hashCodeJsonFileNew = lib.ComputeMd5(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile")
                if (reflect.DeepEqual(pm.hashCodeJsonFileNew, pm.hashCodeJsonFileOld)==false){
                file, _ := ioutil.ReadFile(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile")
                json.Unmarshal(file,&pm.PrintersInTheJsonFile)
                pm.hashCodeJsonFileOld = pm.hashCodeJsonFileNew
             }
				go pm.printJob(job.NativePrinterName, job.Filename, job.Title, job.User, job.JobID, job.Ticket, job.UpdateJob)

			case notification := <-xmppMessages:
				log.Infof("Received XMPP message: %+v", notification)
				if notification.Type == xmpp.PrinterNewJobs {
					if p, exists := pm.printers.GetByGCPID(notification.GCPID); exists {
						go pm.gcp.HandleJobs(&p, func() { pm.incrementJobsProcessed(false) })
					}
				}
			}
		}
	}()
}

func (pm *PrinterManager) incrementJobsProcessed(success bool) {
	pm.jobStatsMutex.Lock()
	defer pm.jobStatsMutex.Unlock()

	if success {
		pm.jobsDone += 1
	} else {
		pm.jobsError += 1
	}
}

// addInFlightJob adds a job ID to the in flight set.
//
// Returns true if the job ID was added, false if it already exists.
func (pm *PrinterManager) addInFlightJob(jobID string) bool {
	pm.jobsInFlightMutex.Lock()
	defer pm.jobsInFlightMutex.Unlock()

	if _, exists := pm.jobsInFlight[jobID]; exists {
		return false
	}

	pm.jobsInFlight[jobID] = struct{}{}

	return true
}

// deleteInFlightJob deletes a job from the in flight set.
func (pm *PrinterManager) deleteInFlightJob(jobID string) {
	pm.jobsInFlightMutex.Lock()
	defer pm.jobsInFlightMutex.Unlock()

	delete(pm.jobsInFlight, jobID)
}

// printJob prints a new job to a native printer, then polls the native job state
// and updates the GCP/Privet job state. then returns when the job state is DONE
// or ABORTED.
//
// All errors are reported and logged from inside this function.
func (pm *PrinterManager) printJob(nativePrinterName, filename, title, user, jobID string, ticket *cdd.CloudJobTicket, updateJob func(string, *cdd.PrintJobStateDiff) error) {
	defer os.Remove(filename)
	if !pm.addInFlightJob(jobID) {
		// This print job was already received. We probably received it
		// again because the first instance is still QUEUED (ie not
		// IN_PROGRESS). That's OK, just throw away the second instance.
		return
	}

	defer pm.deleteInFlightJob(jobID)

	if !pm.jobFullUsername {
		user = strings.Split(user, "@")[0]
	}

	printer, exists := pm.printers.GetByNativeName(nativePrinterName)
	if !exists {
		pm.incrementJobsProcessed(false)
		state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:               cdd.JobStateAborted,
				ServiceActionCause: &cdd.ServiceActionCause{ErrorCode: cdd.ServiceActionCausePrinterDeleted},
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
		return
	}
    pjlEnabled:=false
    pjlCapable:=0
    pjlPrinter:=0
    portName, _ := pm.native.GetPortName(printer.Name)
    for i, k := range pm.PrintersInTheJsonFile.PrintersPjl{
        if (k.PrinterName==nativePrinterName) {
            pjlEnabled=k.PjlEnabled
            pjlCapable=k.PjlCapable
            pjlPrinter=i
        }
    }
    pm.MutexCond.L.Lock()
    pm.printerQue=ExtendPrinterQue(pm.printerQue,portName, jobID)
    for  {
		identidator:=pm.firstInThePrinterQue(portName,jobID)
        if(identidator==0){
             pm.MutexCond.Wait()
        }else if(identidator==2){
		pm.MutexCond.L.Unlock()
		fmt.Printf(jobID+" removed from que exiting")
		return
		}else{
            log.Infof(jobID+"is running")
            break;
        }
   }
    pm.MutexCond.L.Unlock()
    log.Infof(portName + "\n")
	nativeJobID, TotalPages, err  := pm.native.Print(&printer, filename, title, user, jobID, ticket)
	if err != nil {
		if(err.Error()=="Command Restart connector Command Executed"){
			state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateDone,
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
		log.InfoJobf(jobID, "State: %s", state.State.Type)
			os.Exit(1)
		}
		if(err.Error()=="Command invoke Queue Remover Command Executed"){
			pm.invokeQueRemover<-1
			state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateDone,
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
		
		log.InfoJobf(jobID, "State: %s", state.State.Type)
		pm.MutexCond.L.Lock()
        pm.printerQue = RemovePrinterJob(pm.printerQue,portName,jobID)
        pm.MutexCond.L.Unlock()
        pm.MutexCond.Broadcast()
		return
		}
		if(err.Error()=="Command Restart System Command Executed"){
	
			state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateDone,
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
		log.InfoJobf(jobID, "State: %s", state.State.Type)
		pm.MutexCond.L.Lock()
        pm.printerQue = RemovePrinterJob(pm.printerQue,portName,jobID)
        pm.MutexCond.L.Unlock()
      
		h:=exec.Command("cmd","/C","shutdown","/r")
		h.Run()
		return
		}
		if(err.Error()=="Command Restart Spooler"){
				state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateDone,
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
		log.InfoJobf(jobID, "State: %s", state.State.Type)
				pm.native.StopSpoolerService()
				time.Sleep(time.Second*10)
				pm.native.StartSpoolerService()
		}
		pm.incrementJobsProcessed(false)
		log.ErrorJobf(jobID, "Failed to submit to native print system: %s", err)
		state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateAborted,
				DeviceActionCause: &cdd.DeviceActionCause{ErrorCode: cdd.DeviceActionCausePrintFailure},
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob(jobID, err)
		}
	
		return
	}else{
        state := cdd.PrintJobStateDiff{
			State: &cdd.JobState{
				Type:              cdd.JobStateInProgress,
			},
		}
		if err := updateJob(jobID, &state); err != nil {
			log.ErrorJob("Job %s is probably deleted on the cloud", jobID)
		}
    }

	log.InfoJobf(jobID, "Submitted as native job %d + size of job is %d", nativeJobID, TotalPages)
  for  {
	 PrinterRecievedJobState,_ := pm.native.GetJobState(printer.Name,nativeJobID)
	 if(PrinterRecievedJobState.State.Type==cdd.JobStateDone||PrinterRecievedJobState.State.Type==cdd.JobStateAborted){
			log.InfoJobf(jobID, "Is spooled and can't be deleted")
		 break;
	 }
   if err:=pm.gcp.JobLookUp(jobID); err!=nil{
		log.InfoJobf(jobID, "Does not exist in cloud")
  		pm.native.StopSpoolerService()
		  time.Sleep(time.Second*30)
		h:=fmt.Sprintf("C:\\Windows\\System32\\spool\\PRINTERS\\000%d.SPL",nativeJobID)
		if err:=os.Remove(h); err != nil {
			log.ErrorJob("failed to delete SPL for job")
		}
		h=fmt.Sprintf("C:\\Windows\\System32\\spool\\PRINTERS\\000%d.SHD",nativeJobID)
	    
		if err:=os.Remove(h); err != nil {
			log.ErrorJob("failed to delete SHD for job")
		}
		time.Sleep(time.Second*30) //// event
		pm.native.StartSpoolerService()
		pm.MutexCond.L.Lock()
        pm.printerQue = RemovePrinterJob(pm.printerQue,portName,jobID)
        pm.MutexCond.L.Unlock()
        pm.MutexCond.Broadcast()
		return
	}
	time.Sleep(time.Second)
  }
  
	var state cdd.PrintJobStateDiff
    
	ticker := time.NewTicker(time.Second)

    
	for _ = range ticker.C {
      nativeState:= &cdd.PrintJobStateDiff{State: &cdd.JobState{ Type: cdd.JobStateAborted}}
        if(pjlEnabled && pjlCapable==1){
                nativeState, err = pm.native.GetJobStatePJLQuery(title, printer.Name,portName,nativeJobID, TotalPages)
               // nativeState, err = pm.native.GetJobState(printer.Name, nativeJobID)
        }else if (pjlEnabled && pjlCapable==2){
             nativeState, err,pjlCapable = pm.native.TestPrintPjlStateCapabilities(title, printer.Name,portName,nativeJobID, TotalPages)
             if(pjlCapable!=2){
                pm.pjlMutex.Lock()
               pm.PrintersInTheJsonFile.PrintersPjl[pjlPrinter].PjlCapable=pjlCapable
               b, err := json.MarshalIndent(pm.PrintersInTheJsonFile,"","")
              if(err!=nil){
                    return 
              }
              if err = ioutil.WriteFile(os.Getenv("appdata")+"\\Princh\\PrinchPrinterfile", b, 0600); err != nil {
		     return 
	        }
            
                pm.pjlMutex.Unlock()
             }
        }else if(pjlCapable==3){
           nativeState,err= pm.native.GetJobState(printer.Name,nativeJobID)
        }else{
         nativeState = &cdd.PrintJobStateDiff{State: &cdd.JobState{ Type: cdd.JobStateDone}}
         
        }


		if err != nil {
		//	log.WarningJobf(jobID, "Failed to get state of native job %d: %s", nativeJobID, err)

			state = cdd.PrintJobStateDiff{
				State: &cdd.JobState{
					Type:              cdd.JobStateAborted,
					DeviceActionCause: &cdd.DeviceActionCause{ErrorCode: cdd.DeviceActionCauseOther},
				},
				PagesPrinted: state.PagesPrinted,
			}
			if err := updateJob(jobID, &state); err != nil {
				log.ErrorJob(jobID, err)
			}
			pm.incrementJobsProcessed(false)
            pm.MutexCond.L.Lock()
            pm.printerQue = RemovePrinterJob(pm.printerQue,portName, jobID)
            pm.MutexCond.L.Unlock()
            pm.MutexCond.Broadcast()
			log.InfoJobf(jobID, "State: %s", state.State.Type)
			return
		}

		if !reflect.DeepEqual(*nativeState, state) {
			state = *nativeState
			if err = updateJob(jobID, &state); err != nil {
				log.ErrorJob(jobID, err)
			}
            pm.MutexCond.L.Lock()
               pm.printerQue = RemovePrinterJob(pm.printerQue,portName,jobID)
               pm.MutexCond.L.Unlock()
               pm.MutexCond.Broadcast()
			log.InfoJobf(jobID, "State: %s", state.State.Type)
		}

		if state.State.Type != cdd.JobStateInProgress && state.State.Type != cdd.JobStateStopped {
			if state.State.Type == cdd.JobStateDone {
				pm.incrementJobsProcessed(true)
			} else {
				pm.incrementJobsProcessed(false)
			}
			return
		}
	}
}


// GetJobStats returns information that is useful for monitoring
// the connector.
func (pm *PrinterManager) GetJobStats() (uint, uint, uint, error) {
	var processing uint

	for _, printer := range pm.printers.GetAll() {
		processing += printer.NativeJobSemaphore.Count()
	}

	pm.jobStatsMutex.Lock()
	defer pm.jobStatsMutex.Unlock()

	return pm.jobsDone, pm.jobsError, processing, nil
}



//Extends Printerque by one printerJob
func ExtendPrinterQue(slice []printerQue, element string, jobId string) []printerQue {

    n := len(slice)
    if n == cap(slice) {
        // Slice is full; must grow.
        // We double its size and add 1, so if the size is zero we still grow.
        newSlice := make([]printerQue, len(slice), 2*len(slice)+1)
        copy(newSlice, slice)
        slice = newSlice
    }
    slice = slice[0 : n+1]
    slice[n].portName = element
    slice[n].jobId = jobId
    return slice
}



//RemovePrinterJob removes a printer job from the printerQue
func  RemovePrinterJob(slice []printerQue, portName string, jobId string) []printerQue {
    for i, b := range slice {
        if(b.portName==portName && b.jobId==jobId){
           printerQue := append(slice[:i], slice[i+1:]...)
           return printerQue 
        }
    }
    
   return slice
}

//firstInThePrinterQue finds out if a element is first in slice
func (pm *PrinterManager) firstInThePrinterQue(portName string,jobId string ) uint {
    for _, b := range pm.printerQue {
        if b.portName == portName {
            if(b.jobId != jobId){
            return 0
            }else{
            return 1
            }
        }
    }
    return 2
}
/// Checks printerjobs on the cloud every 15 minutes and if a command have been invoked. deleting a job from the que, means if it have not been sent to the printer yet it will be deleted and not be sent to the printer at all
func (pm *PrinterManager) QueRemover() {
	ticker := time.Tick(time.Minute*15)
	for{
		select{
			case h:=<-pm.invokeQueRemover:{
				if	(h==1){
					fmt.Println("Command invoked queRemover")
					for _,b := range pm.printerQue{
						if err:=pm.gcp.JobLookUp(b.jobId); err!=nil{
							pm.MutexCond.L.Lock()
							pm.printerQue=RemovePrinterJob(pm.printerQue,b.portName,b.jobId) 
							fmt.Printf(b.jobId + "removed from the job que")
							pm.MutexCond.L.Unlock()
						}
						pm.MutexCond.Broadcast()	
					}
				}
				break
			}
			case <-ticker:{
			
				for _,b := range pm.printerQue{
					if err:=pm.gcp.JobLookUp(b.jobId); err!=nil{
					pm.MutexCond.L.Lock()
					pm.printerQue=RemovePrinterJob(pm.printerQue,b.portName,b.jobId) 
					fmt.Printf(b.jobId + "removed from the job que")
					pm.MutexCond.L.Unlock()
					}
				pm.MutexCond.Broadcast()	
				}
			}
		}
	}
}

//Handles the logfile deletes the logfile and makes a copy of it in terms of oldlog.txt, when the log file have been copied a new logfile will be created
//and the output of the log will be set to the new logfile                                                                                                                   
func (pm *PrinterManager)HandleLogFile(){

	
	
	
	d,_:=os.Open(os.Getenv("appdata")+"\\Princh")
	fi, _ := d.Readdir(-1)
	 if(pm.logFileNewestDate==""){
		 StringExistInFolder:= log.StringInFileInfo(fi,"PrinchConnectorLog")
		if(StringExistInFolder!=""){
			fmt.Println(StringExistInFolder)
			pm.logFileNewestDate=StringExistInFolder
		}	 
		 
		 
		 
	 }
	 for _, fi := range fi {
        if fi.Mode().IsRegular() {
		
			if(strings.HasPrefix(fi.Name(),"PrinchConnectorLog")){
           FileDate:=strings.TrimPrefix(fi.Name(),"PrinchConnectorLog")
		   FileDate=strings.TrimSuffix(FileDate,".txt")
		
		   
         
		  if(FileDate!=""){
			
			t21:=log.GetTime(FileDate)
			t20:=time.Now()	
			t22:=t20.Sub(t21)
			fmt.Println(t22)
			OneWeek,_:=time.ParseDuration("168h")
			//TwoWeeks,_:=time.ParseDuration("336h")
			
			if(t22.Hours()<OneWeek.Hours()){
			  year,month,day:=t21.Date()
			  
			  Date:=fmt.Sprint(year,month,day)
			  pm.logFileNewestDate=os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+Date+".txt"
			}else if(t22.Hours()>OneWeek.Hours()){
		 	
			  year,month,day:=t21.Date()
			  Date:=fmt.Sprint(year,month,day)
			  os.Remove(os.Getenv("appdata")+"\\Princh\\OldLog.txt")
			  os.Link(os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+Date+".txt",os.Getenv("appdata")+"\\Princh\\OldLog.txt")
			  os.Remove(os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+Date+".txt")
		
			  FindCurrentTime:=time.Now()
			  year,month,day=FindCurrentTime.Date()
			  CurrentDate:=fmt.Sprint(year,month,day)
			  if(pm.logFileNewestDate!=os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+CurrentDate+".txt"){
			  os.Create(os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+CurrentDate+".txt")
			  pm.logFileNewestDate=os.Getenv("appdata")+"\\Princh\\PrinchConnectorLog"+CurrentDate+".txt"
			  log.SetPath(pm.logFileNewestDate) 
			 }
			  
			}
		  }
    }
}
}
fmt.Println("newest date is:"+pm.logFileNewestDate)
d.Close()
}


