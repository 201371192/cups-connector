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
	
	"time"
    "sync"
    "io"
    "io/ioutil"
	"crypto/md5"
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
}
type PrintersInJsonFile struct {
	LocalPrintingEnable bool `json:"local_printing_enable"`
	CloudPrintingEnable bool `json:"cloud_printing_enable"`
	XmppJid string `json:"xmpp_jid"`
	RobotRefreshToken string `json:"robot_refresh_token"`
	UserRefreshToken string `json:"user_refresh_token"`
	ShareScope string `json:"share_scope"`
	ProxyName string `json:"proxy_name"`
	PrinterBlacklist []string `json:"printer_blacklist"`
	LogLevel string `json:"log_level"`
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

	quit chan struct{}
}

func NewPrinterManager(native NativePrintSystem, gcp *gcp.GoogleCloudPrint, privet *privet.Privet, printerPollInterval time.Duration, nativeJobQueueSize uint, jobFullUsername bool, shareScope string, jobs <-chan *lib.Job, xmppNotifications <-chan xmpp.PrinterNotification) (*PrinterManager, error) {
	var printers *lib.ConcurrentPrinterMap
	var queuedJobsCount map[string]uint
    var conf PrintersInJsonFile
	var err error

    file, err := ioutil.ReadFile("gcp-windows-connector.config.json")
     if (err!=nil){
      json.Unmarshal(file,&conf)
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

        PrintersInTheJsonFile: conf,
        pjlMutex:           sync.Mutex{},
		jobsInFlightMutex: sync.Mutex{},
		jobsInFlight:      make(map[string]struct{}),
        printerQue:            []printerQue{}, 
        MutexCond:                *c,
		nativeJobQueueSize: nativeJobQueueSize,
		jobFullUsername:    jobFullUsername,
		shareScope:         shareScope,

		quit: make(chan struct{}),
	}

	// Sync once before returning, to make sure things are working.
	// Ignore privet updates this first time because Privet always starts
	// with zero printers.
	if err = pm.syncPrinters(true); err != nil {
		return nil, err
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

func (pm *PrinterManager) syncPrinters(ignorePrivet bool) error {
	log.Info("Synchronizing printers, stand by")
	pm.hashCodeJsonFileNew = computeMd5("gcp-windows-connector.config.json")
    var PrintersInCloud,_,_=pm.gcp.ListPrinters()

	// Get current snapshot of native printers.
	nativePrinters, err := pm.native.GetPrinters()
   
    if err != nil {
		return fmt.Errorf("Sync failed while calling GetPrinters(): %s", err)
	}
 
    if (reflect.DeepEqual(pm.hashCodeJsonFileNew, pm.hashCodeJsonFileOld)==false){
    file, err := ioutil.ReadFile("gcp-windows-connector.config.json")
     
      json.Unmarshal(file,&pm.PrintersInTheJsonFile)
     
     if err != nil {
		return fmt.Errorf("Sync failed while calling readFile(): %s", err)
	}
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
    
    b, err := json.MarshalIndent(pm.PrintersInTheJsonFile,"","")
     if(err!=nil){
         return fmt.Errorf("failed to marshal pm.PrintersInTheJsonFile: %s",err)
     }
   if err = ioutil.WriteFile("gcp-windows-connector.config.json", b, 0600); err != nil {
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
                pm.hashCodeJsonFileNew = computeMd5("gcp-windows-connector.config.json")
                if (reflect.DeepEqual(pm.hashCodeJsonFileNew, pm.hashCodeJsonFileOld)==false){
                file, _ := ioutil.ReadFile("gcp-windows-connector.config.json")
                json.Unmarshal(file,&pm.PrintersInTheJsonFile)
                pm.hashCodeJsonFileOld = pm.hashCodeJsonFileNew
             }
				go pm.printJob(job.NativePrinterName, job.Filename, job.Title, job.User, job.JobID, job.Ticket, job.UpdateJob)

			case notification := <-xmppMessages:
				log.Debugf("Received XMPP message: %+v", notification)
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
    pm.printerQue=Extend(pm.printerQue,portName, jobID)
    for (stringInSlice(portName,pm.printerQue)) {
        if(!firstInSlice(portName,jobID,pm.printerQue)){
             pm.MutexCond.Wait()
        }else{
            log.Infof(jobID+"is running")
            break;
        }
   }
    pm.MutexCond.L.Unlock()
    log.Infof(portName + "\n")
	nativeJobID, TotalPages, err  := pm.native.Print(&printer, filename, title, user, jobID, ticket)
	if err != nil {
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
			log.ErrorJob(jobID, err)
		}
    }

	log.InfoJobf(jobID, "Submitted as native job %d + size of job is %d", nativeJobID, TotalPages)
    
	var state cdd.PrintJobStateDiff
    
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
  
    
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
              if err = ioutil.WriteFile("gcp-windows-connector.config.json", b, 0600); err != nil {
		     return 
	        }
            
                pm.pjlMutex.Unlock()
             }
        }else if(pjlCapable==3){
           nativeState,err= pm.native.GetJobState(printer.Name,jobID)
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
            pm.printerQue = Remove(pm.printerQue,portName, jobID)
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
               pm.printerQue = Remove(pm.printerQue,portName,jobID)
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

func computeMd5(filePath string) ([]byte){
	var result []byte 
	var err error
	file, err := os.Open(filePath)
	if err != nil {
		return result
	}
	defer file.Close()
	hash:=md5.New()
	if _, err := io.Copy(hash,file); err != nil {
		return result
	}
	return hash.Sum(result)
}

//Extend slice by element
func Extend(slice []printerQue, element string, jobId string) []printerQue {

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

//stringInSlice finds out if a element exist in slice
func stringInSlice(a string, list []printerQue) bool {
    for _, b := range list {
        if b.portName == a {
            return true
        }
    }
    return false
}

//Remove a element from slice
func  Remove(slice []printerQue, portName string, jobId string) []printerQue {
    for i, b := range slice {
        if(b.portName==portName && b.jobId==jobId){
           printerQue := append(slice[:i], slice[i+1:]...)
           return printerQue 
        }
    }
    
   return slice
}

//firstInSlice finds out if a element is first in slice
func firstInSlice(portName string,jobId string, list []printerQue ) bool {
    for _, b := range list {
        if b.portName == portName {
            if(b.jobId != jobId){
            return false
            }else{
            return true
            }
        }
    }
    return false
}