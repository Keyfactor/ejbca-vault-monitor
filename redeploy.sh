#!/bin/sh

helpFunction()
{
   echo ""
   echo "Usage: $0 -b parameterB "
   echo -e "\t-b Build number version for the plugin, this should be incremented after each build, e.g. 1"
   exit 1 # Exit script after printing help
}

while getopts "b:" opt
do
   case "$opt" in
      b ) parameterB="$OPTARG" ;;
      ? ) helpFunction ;; # Print helpFunction in case parameter is non-existent
   esac
done

# Print helpFunction in case parameters are empty
if [ -z "$parameterB" ] 
then
   echo "Some or all of the parameters are empty";
   helpFunction
fi

# Build the plugin
go build -o out/ejbca-vault-monitor-v$parameterB
retVal=$?
if [ $retVal -ne 0 ]; then
    echo "Error compiling Go"
    exit $retVal
fi

SHA256=`sha256sum out/ejbca-vault-monitor-v$parameterB | awk '{ print $1 }'`
echo "SHA256: $SHA256"

# Disable existing plugin and register and enable new version
vault secrets disable pki
vault plugin register -sha256="${SHA256}" secret ejbca-vault-monitor-v$parameterB
vault secrets enable -path=pki -plugin-name=ejbca-vault-monitor-v$parameterB plugin

#
# Enable and enroll
#

# Create a Vault CA
#vault write pki/root/generate/internal common_name="Vault Test Root CA" ttl=8760h

# Configure a role for enrollment, only logging to Vault log
#vault write pki/roles/test-role generate_lease=true ttl=1h max_ttl=1h allow_any_name=true 
# Configure a role for enrollment, calling clientToolBox to log the issuance in EJBCA
#vault write pki/roles/test-role generate_lease=true ttl=1h max_ttl=1h allow_any_name=true external_cmd="/home/user/clientToolBox/ejbcaClientToolBox.sh, EjbcaWsRaCli, customlog, INFO, VAULT_CERT, Certificate issued in Vault, ManagementCA, COMMONNAME, CERTFILE"

# Issue the certificate
#vault write pki/issue/test-role common_name="test.forbidden.org" alt_names="test-1.forbidden.org,test-2.forbidden.org"

# View the import queue
#vault list pki/import-queue

