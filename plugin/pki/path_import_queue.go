package pki

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/hashicorp/vault/sdk/framework"
	hconsts "github.com/hashicorp/vault/sdk/helper/consts"
	"github.com/hashicorp/vault/sdk/logical"
	"log"
	"strings"
	"sync"
	"time"
	"io/ioutil"
	"os"
	"os/exec"
)

//Jobs tructure for import queue worker
type Job struct {
	id         int
	entry      string
	roleName   string
	policyName string
	importPath string
	ctx        context.Context
	storage logical.Storage
}

// This returns the list of queued for import
func pathImportQueue(b *backend) *framework.Path {
	ret := &framework.Path{
		Pattern: "import-queue/" + framework.GenericNameRegex("role"),

		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ReadOperation: b.pathUpdateImportQueue,
			//TODO: add delete operation to stop import queue and delete it
			//TODO: add delete operation to delete particular import record

		},

		HelpSynopsis:    pathImportQueueSyn,
		HelpDescription: pathImportQueueDesc,
	}
	ret.Fields = addNonCACommonFields(map[string]*framework.FieldSchema{})
	return ret
}

func pathImportQueueList(b *backend) *framework.Path {
	ret := &framework.Path{
		Pattern: "import-queue/",
		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ListOperation: b.pathFetchImportQueueList,
		},

		HelpSynopsis:    pathImportQueueSyn,
		HelpDescription: pathImportQueueDesc,
	}
	return ret
}

func (b *backend) pathFetchImportQueueList(ctx context.Context, req *logical.Request, data *framework.FieldData) (response *logical.Response, retErr error) {
	roles, err := req.Storage.List(ctx, "import-queue/")
	var entries []string
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		log.Printf("Getting entry %s", role)
		rawEntry, err := req.Storage.List(ctx, "import-queue/"+role)
		if err != nil {
			return nil, err
		}
		var entry []string
		for _, e := range rawEntry {
			entry = append(entry, fmt.Sprintf("%s: %s", role, e))
		}
		entries = append(entries, entry...)
	}
	return logical.ListResponse(entries), nil
}

func (b *backend) pathUpdateImportQueue(ctx context.Context, req *logical.Request, data *framework.FieldData) (response *logical.Response, retErr error) {
	roleName := data.Get("role").(string)
	log.Printf("Using role: %s", roleName)

	entries, err := req.Storage.List(ctx, "import-queue/"+data.Get("role").(string)+"/")
	if err != nil {
		return nil, err
	}

	return logical.ListResponse(entries), nil
}

func (b *backend) fillImportQueueTask(roleName string, noOfWorkers int, storage logical.Storage, conf *logical.BackendConfig) {
	ctx := context.Background()
	jobs := make(chan Job, 100)
	replicationState := conf.System.ReplicationState()
	//Checking if we are on master or on the stanby Vault server
	isSlave := !(conf.System.LocalMount() || !replicationState.HasState(hconsts.ReplicationPerformanceSecondary)) ||
		replicationState.HasState(hconsts.ReplicationDRSecondary) ||
		replicationState.HasState(hconsts.ReplicationPerformanceStandby)
	if isSlave {
		log.Printf("We're on slave. Sleeping")
		return
	}
	log.Printf("We're on master. Starting to import certificates")
	//var err error
	importPath := "import-queue/" + roleName + "/"

	entries, err := storage.List(ctx, importPath)
	if err != nil {
		log.Printf("Could not get queue list from path %s: %s", err, importPath)
		return
	}
	log.Printf("Queue list on path %s has length %v", importPath, len(entries))

	var wg sync.WaitGroup
	wg.Add(noOfWorkers)
	for i := 0; i < noOfWorkers; i++ {
		go func() {
			defer func() {
				r := recover()
				if r != nil {
					log.Printf("Recover %s", r)
				}
				wg.Done()
			}()
			for job := range jobs {
				result := b.processImport(job)
				log.Printf("Job id: %d ### Processed entry: %s , result:\n %v\n", job.id, job.entry, result)
			}
		}()
	}
	for i, entry := range entries {
		log.Printf("Allocating job for entry %s", entry)
		job := Job{
			id:         i,
			entry:      entry,
			importPath: importPath,
			roleName:   roleName,
			storage:    storage,
			ctx:        ctx,
		}
		jobs <- job
	}
	close(jobs)
	wg.Wait()
}

func (b *backend) startImportControler(conf *logical.BackendConfig) {

	log.Printf("Starting importcontroler")
	b.taskStorage.register("importcontroler", func() {
		b.controlImportQueue(conf)
	}, 1, time.Second*1)
}

func (b *backend) controlImportQueue(conf *logical.BackendConfig) {
	log.Printf("Running control import queue")
	ctx := context.Background()
	const fillQueuePrefix = "fillqueue-"
	roles, err := b.storage.List(ctx, "role/")
	if err != nil {
		log.Printf("Couldn't get list of roles %s", err)
		return
	}
	for i := range roles {
		roleName := roles[i]
		log.Printf("Processing role %s", roleName)
		//Update role since it's settings may be changed
		role, err := b.getRole(ctx, b.storage, roleName)
		if err != nil {
			log.Printf("Error getting role %v: %s\n Exiting.", role, err)
			continue
		}

		if role == nil {
			log.Printf("Unknown role %v\n", role)
			continue
		}
		b.taskStorage.register(fillQueuePrefix+roleName, func() {
			log.Printf("Run queue filler %s", roleName)
			b.fillImportQueueTask(roleName, 1, b.storage, conf) // 1 = number of workers
		}, 1, time.Duration(5)*time.Second)
	}
	stringInSlice := func(s string, sl []string) bool {
		for i := range sl {
			if sl[i] == s {
				return true
			}
		}
		return false
	}
	for _, taskName := range b.taskStorage.getTasksNames() {
		if strings.HasPrefix(taskName, fillQueuePrefix) && !stringInSlice(strings.TrimPrefix(taskName, fillQueuePrefix), roles) {
			b.taskStorage.del(taskName)
		}
	}
	log.Printf("Finished running control import queue")
}

func (b *backend) processImport(job Job) string {

	msg := fmt.Sprintf("Job id: %v ###", job.id)
	importPath := job.importPath
	log.Printf("%s Trying to import certificate with SN %s", msg, job.entry)

	certEntry, err := job.storage.Get(job.ctx, importPath+job.entry)
	if err != nil {
		return fmt.Sprintf("%s Could not get certificate from %s: %s", msg, importPath+job.entry, err)
	}
	if certEntry == nil {
		return fmt.Sprintf("%s Could not get certificate from %s: cert entry not found", msg, importPath+job.entry)
	}
	block := pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certEntry.Value,
	}

	Certificate, err := x509.ParseCertificate(certEntry.Value)
	if err != nil {
		return fmt.Sprintf("%s Could not get certificate from entry %s: %s", msg, importPath+job.entry, err)
	}
	//TODO: here we should check for existing CN and set it to DNS or throw error
	cn := Certificate.Subject.CommonName
	sn := Certificate.SerialNumber

	certString := string(pem.EncodeToMemory(&block))
	log.Printf("%s Importing cert to %s:\n", msg, cn)

	// The actual import processing is done here
	// Our first option is just to log the issued certificate to the Vault debug log
	log.Printf("Certificate issued:%s:%x", cn, sn)
	log.Printf("Certificate:%s\n", certString)

	// Second we have an option to call an external script with our issued certificate
	// TODO: configure the execution string as a separate config for the plugin
	// Create our Temp File
	roleName := job.roleName
	ctx := context.Background()
	role, err := b.getRole(ctx, b.storage, roleName)
	externalcmd := role.ExternalCmd
	log.Printf("external_cmd: %s", externalcmd);
	if len(externalcmd) > 0 {
		tmpFile, err := ioutil.TempFile(os.TempDir(), "vault-certificate-")
		if err != nil {
			log.Fatal("Cannot create temporary file", err)
			return fmt.Sprintf("Cannot create temporary file %s", err)
		}
		// Remember to clean up the file afterwards
		defer os.Remove(tmpFile.Name())
		if _, err = tmpFile.WriteString(certString); err != nil {
			log.Fatal("Failed to write to temporary file '%s', %s", tmpFile.Name(), err)
			return fmt.Sprintf("Failed to write to temporary file '%s', %s", tmpFile.Name(), err)
		}
		if err := tmpFile.Close(); err != nil {
			log.Fatal(err)
			return fmt.Sprintf("Cannot close temporary file %s", err)
		}
	
		// Read the role, to see what we have configured	
		for i, s := range externalcmd {
			if ("CERTFILE" == s) {
				log.Printf("Replacing CERTFILE with: %s\n", tmpFile.Name())
				externalcmd[i] = tmpFile.Name()
			}
			if ("COMMONNAME" == s) {
				log.Printf("Replacing COMMONNAME with %s\n", cn)
				externalcmd[i] = cn
			}
		}

		// add all attributes
		cmd := &exec.Cmd {
			Path: externalcmd[0],
			Args: externalcmd,
		}
		// Log command we will run
		log.Printf("Running command: %s\n", cmd.String())
		// run command
		if output, err := cmd.Output(); err != nil {
			log.Printf("An error happened running import command, not removing from queue: %s", err);
			return fmt.Sprintf("An error happened running import command, not removing from queue: %s:%x:%d", cn, sn, err)
		} else {
			log.Printf( "Command output: %s\n", output )
		}
	}
	// Third, we could call a REST API, sending the issued certificate to another service
	// TODO: no implemented

	// After importing, delete the record from the queue
	b.deleteCertFromQueue(job)

	return fmt.Sprintf("Imported certificate for '%s', with serial number %x", cn, sn)
}

func (b *backend) deleteCertFromQueue(job Job) {

	msg := fmt.Sprintf("Job id: %v ###", job.id)
	importPath := job.importPath
	log.Printf("%s Removing certificate from import path %s", msg, importPath+job.entry)
	err := job.storage.Delete(job.ctx, importPath+job.entry)
	if err != nil {
		log.Printf("%s Could not delete %s from queue: %s", msg, importPath+job.entry, err)
	} else {
		log.Printf("%s Certificate with SN %s removed from queue", msg, job.entry)
		_, err := job.storage.List(job.ctx, importPath)
		if err != nil {
			log.Printf("%s Could not get queue list: %s", msg, err)
		}
	}
}

func (b *backend) cleanupImportQueue(roleName string, ctx context.Context, req *logical.Request) {

	importPath := "import-queue/" + roleName + "/"
	entries, err := req.Storage.List(ctx, importPath)
	if err != nil {
		log.Printf("Could not read from queue: %s", err)
	}
	for _, sn := range entries {
		err = req.Storage.Delete(ctx, importPath+sn)
		if err != nil {
			log.Printf("Could not delete %s from queue: %s", importPath+sn, err)
		} else {
			log.Printf("Deleted %s from queue", importPath+sn)
		}
	}

}

const pathImportQueueSyn = `
Fetch a CA, CRL, CA Chain, or non-revoked certificate.
`

const pathImportQueueDesc = `
This allows certificates to be fetched. If using the fetch/ prefix any non-revoked certificate can be fetched.

Using "ca" or "crl" as the value fetches the appropriate information in DER encoding. Add "/pem" to either to get PEM encoding.

Using "ca_chain" as the value fetches the certificate authority trust chain in PEM encoding.
`
