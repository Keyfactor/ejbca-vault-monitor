![PrimeKey](primekey_logo.png)

# Community supported 
We welcome contributions.
 
ejbca-vault-plugin is open source and community supported, meaning that there is **no SLA** applicable for this tool.

To report a problem or suggest a new feature, use the **[Issues](../../issues)** tab. If you want to contribute actual bug fixes or proposed enhancements, use the **[Pull requests](../../pulls)** tab.

# License
MPL-2.0 License.

# PKI Monitoring Secrets Engine for HashiCorp Vault

This solution allows [HashiCorp Vault](https://www.vaultproject.io/) users to provide their
Information Security organization visibility into certificate issuance.
Vault issued certificates are automatically logged according to the configuration.

The plugin is based on the [Venafi Vault PKI Monitor](https://github.com/Venafi/vault-pki-monitor-venafi), which in turn has sourced the [secrets engine](https://www.vaultproject.io/docs/secrets/pki/index.html) component from the original [HashiCorp Vault PKI secrets engine](https://github.com/hashicorp/vault/tree/master/builtin/logical/pki).

## Build

The plugin is written in [Go](https://golang.org/dl/), as that is the language chosen by Vault.

Installing Go on an Ubuntu Linux systems can easily be done by:
```
> sudo snap install go
```
After that you can build the plugin.

Build the plugin, using Go, with the following command:

```
> go build -o out/ejbca-vault-monitor-v1
```

This will build the plugin and store the resulting executable as `out/ejbca-vault-monitor-v1`


## Installation

The Vault Monitor plugin is installed like other [Vault plugins](https://www.vaultproject.io/docs/internals/plugins.html). The plugin executable must reside in the Vault plugin directory, and the SHA256 hash of the plugin executable must be known to the administrator installing the plugin.

### Start Vault
Managing Vault in a production enviroinment is outside the scope of this instruction, so we start a development server with an appointed plugin directory (straight into our build directory), and a defined root token, change this root token to something random of your own:

```
vault server -dev -dev-root-token-id=gUgvfVcVzdKH -dev-plugin-dir=/home/user/git/ejbca-vault-monitor-v1/out -log-level=debug
```

To use the vault CLI you need to set environment variables:

```
export VAULT_ADDR='http://127.0.0.1:8200'
export VAULT_TOKEN='gUgvfVcVzdKH'
```
Now you are ready to run Vault CLI commands.

### Register and enable plugin

To register the plugin you must first get the SHA256 hash of the plugin executable, after which you can register the plugin with Vault:

```
SHA256=$(sha256sum /home/user/git/ejbca-vault-monitor/out/ejbca-vault-monitor-v1| cut -d' ' -f1)
vault plugin register -sha256="${SHA256}" secret ejbca-vault-monitor-v1
```

`"${SHA256}"` is the SHA256 of the Vault plugin executable, and can be specified manually on the command line if you prefer that instead of using a shell variable. 

After registration you can enable the plugin, which gives a path for further commands to vault to use the plugin. Use the default Vault CA pki path:

```
vault secrets enable -path=pki -plugin-name=ejbca-vault-monitor-v1 plugin
```

### Disable plugin
If you want to disable the plugin, without re-registering it, that can easily be done:

```
vault secrets disable pki
```

Note that when running Vault in Dev mode (as the example command above) disabling the plugin will remove all the certificates queued, i.e. the 'list' command below will return empty. When upgrading the plugin in production another method is recommended which is to include the version of the plugin as -vX where X is the version of the plugin.  Details on Hashicorp plugin upgrade are documented at [Upgrading Vault plugins](https://www.vaultproject.io/docs/upgrading/plugins).

### Rebuild and re-deploy
Example command how to perform a full rebuild - redeploy cycle (for example when modifying the plugin) can be found in the `redeploy.sh` file in this repo.

## Configuration

There is currently no configuration to be done.

## Usage

To issue a new certificate, use the built in Vault CA, to either issue a private key + certificate, or enroll with a CSR. When a certificate has been issued, it is placed on the import queue for further processing.

Create a CA:

```
vault write pki/root/generate/internal common_name="Vault Test Root CA" ttl=8760h
```

Create a, very permissive, role:

```
vault write pki/roles/test-role generate_lease=true ttl=1h max_ttl=1h allow_any_name=true
```

Issue a certificate, having Vault generate the private key:

```
vault write pki/issue/test-role common_name="test.forbidden.org" alt_names="test-1.forbidden.org,test-2.forbidden.org"
```

## Import Queue
After a certificate has been signed by the Vault CA it is added to the import queue. Processing of certificates in the queue begins automatically and will run continuously from that point until the plugin exits.

You can view the contents of the import queue (by certificate serial number) using the following command:
```
vault list pki/import-queue
```
Note that the import queue is normally empty unless there is an error processing the queue, as processing entries are very fast.

You can check certificates for a specific role by running:
```
vault read pki/import-queue/<ROLE_NAME>
```
## Import Queue Processing

Processing on the import queue can perform various tasks.
* It always logs to the Vault log
* If an external command has been configured, the external command is called with the certificate
* If a REST API endpoint has been configured, the external REST point is called with the certificate (not implemented yet)

To configure the import queue to send certificates to an external script:
```
vault write pki/roles/test-role generate_lease=true ttl=1h max_ttl=1h allow_any_name=true external_cmd="/home/user/clientToolBox/ejbcaClientToolBox.sh, EjbcaWsRaCli, customlog, INFO, VAULT_CERT, Certificate issued in Vault, ManagementCA, COMMONNAME, CERTFILE"
```

The external_cmd line is a comma separated list of command line arguments for executing the command. The variables COMMONNAME and CERTFILE will be replaced by the CN from the issued cert and a path to a temporary file with the issued certificate.

If no external_cmd is defined, no script will be called, but logging to Vault log will aways happen.


# Considerations
The plugin is a modified version of the built in Vault PKI module That means that if you want to use this plugin, instead of the built in Vault PKI plugin, you need to make sure that your teams can not use the built in Vault module, as that will bypass this monitoring plugin.
Being a modified version of the built in Vault module, it's important to keep track of the differences between this version and the Vault version.
Modified file, as of rebase in October 2020:
* backend.go - add import paths and start import queue
* path_issue_sign-go - add to import queue when certificate is issued
* path_roles.go - add name and external_cmd to role, clean import queue if role deleted
* scheduler.go - the scheduler for running the import queue processing
* cert-util.go - comment out a template section due to compilation error

The PKI code can always be compared easily by comparing this plugin code with https://github.com/hashicorp/vault/tree/master/builtin/logical/pki.
