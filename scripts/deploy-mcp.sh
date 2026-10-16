#!/bin/sh
# Stage local, reviewable artifacts. This script never activates a host service.
set -eu
exec python3 - "$@" <<'PY'
import argparse, hashlib, json, os, pathlib, plistlib, re, shutil, sys
p=argparse.ArgumentParser(description='Stage Coop MCP artifacts without activation')
p.add_argument('--stage',action='store_true',required=True)
p.add_argument('--host',required=True)
p.add_argument('--binary',required=True)
p.add_argument('--config',required=True)
p.add_argument('--destination',required=True)
p.add_argument('--install-dir')
p.add_argument('--runtime-bin',required=True,help='Absolute Container executable path on the target host; validated on the staging host')
a=p.parse_args()
if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9.-]{0,127}',a.host):p.error('invalid host')
binary=pathlib.Path(a.binary)
config=pathlib.Path(a.config)
destination=pathlib.Path(a.destination)
if not binary.is_file() or binary.is_symlink() or not config.is_absolute() or not destination.is_absolute():p.error('absolute destination/config and regular binary required')
if destination.exists() and (destination.is_symlink() or any(destination.iterdir())):p.error('stage destination must be new or empty')
runtime=pathlib.Path(a.runtime_bin)
if not runtime.is_absolute() or runtime.name!='container' or not runtime.is_file() or not os.access(runtime,os.X_OK):p.error('absolute executable Container path required')
sha=hashlib.sha256(binary.read_bytes()).hexdigest()
install=pathlib.Path(a.install_dir) if a.install_dir else pathlib.Path.home()/'.local/lib/coop/mcp'/sha
if not install.is_absolute():p.error('absolute install directory required')
destination.mkdir(mode=0o700,parents=True,exist_ok=True)
os.chmod(destination,0o700)
shutil.copyfile(binary,destination/'coop')
os.chmod(destination/'coop',0o700)
logs=config.parent/'logs'
plist={'EnvironmentVariables':{'PATH':str(runtime.parent)+':/usr/bin:/bin:/usr/sbin:/sbin'},'Label':'com.sarcasticbird.coop.mcp','ProgramArguments':[str(install/'coop'),'mcp','serve','--config',str(config)],'WorkingDirectory':str(config.parent),'RunAtLoad':True,'KeepAlive':True,'ProcessType':'Background','StandardOutPath':str(logs/'service.log'),'StandardErrorPath':str(logs/'error.log'),'Umask':0o077,'ThrottleInterval':10}
with open(destination/'coop-mcp.plist','wb') as f:plistlib.dump(plist,f)
os.chmod(destination/'coop-mcp.plist',0o600)
packet={'version':1,'host':a.host,'sha256':sha,'config':str(config),'runtime_bin':str(runtime),'install_dir':str(install),'label':plist['Label'],'activated':False,'route_configured':False,'requires_review':['caller grants and token sources','runtime availability','existing launchd job and listener inventory','dedicated private Tailscale Serve route','client bearer support and guest reachability'],'rollback':'Unload only the new MCP launchd job and remove only its reviewed private Serve route; preserve the operation ledger.'}
with open(destination/'activation.json','w') as f:json.dump(packet,f,indent=2);f.write('\n')
os.chmod(destination/'activation.json',0o600)
print(json.dumps({'stage':str(destination),'host':a.host,'sha256':sha,'activated':False}))
PY
