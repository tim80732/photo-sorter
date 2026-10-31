# For development: builds the Docker image from this checkout on an Unraid
# server and (re)starts the container. To just install the app, use the
# published image instead (see README).
#   .\deploy-unraid.ps1 -Ssh <ssh host alias> -HostIp <server's LAN IP>
# Run .\build.ps1 first. Uses: /mnt/user/appdata/photo-sorter/{build,config}
param([Parameter(Mandatory)][string]$Ssh, [Parameter(Mandatory)][string]$HostIp, [int]$Port = 8095)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not (Test-Path dist\linux\photosorter)) { throw "run .\build.ps1 first" }

$app = "/mnt/user/appdata/photo-sorter"
ssh $Ssh "mkdir -p $app/build $app/config && chown 99:100 $app/config"
if (-not $?) { throw "ssh to $Ssh failed" }
scp -q dist\linux\photosorter docker\Dockerfile winres\icon.png "${Ssh}:$app/build/"
if (-not $?) { throw "copy failed" }
$tmpl = (Get-Content docker\photo-sorter.xml -Raw).Replace("__HOST__", $HostIp).Replace("8095", "$Port") -replace "`r", ""
[IO.File]::WriteAllText("$env:TEMP\my-photo-sorter.xml", $tmpl)
scp -q "$env:TEMP\my-photo-sorter.xml" "${Ssh}:/boot/config/plugins/dockerMan/templates-user/my-photo-sorter.xml"

# Same settings as the template, so the Docker tab's Edit works later.
$script = @"
set -e
cd $app/build
chmod 755 photosorter
docker build -q -t photo-sorter:latest . >/dev/null
TZ=`$(readlink /etc/localtime | sed 's#.*/zoneinfo/##')
docker rm -f photo-sorter >/dev/null 2>&1 || true
docker run -d --name photo-sorter --restart unless-stopped --user 99:100 \
  -e TZ="`$TZ" -e HOST_OS=Unraid -e HOST_CONTAINERNAME=photo-sorter \
  -p ${Port}:8095 \
  -v /mnt/user/photos/camera-photo:/camera-photo:rw -v $app/config:/config:rw \
  -l net.unraid.docker.managed=dockerman \
  -l 'net.unraid.docker.webui=http://[IP]:[PORT:8095]/' \
  -l net.unraid.docker.icon=http://${HostIp}:${Port}/icon.png \
  photo-sorter:latest >/dev/null
mkdir -p /var/lib/docker/unraid/images /usr/local/emhttp/state/plugins/dynamix.docker.manager/images
cp icon.png /var/lib/docker/unraid/images/photo-sorter-icon.png
cp icon.png /usr/local/emhttp/state/plugins/dynamix.docker.manager/images/photo-sorter-icon.png
docker image prune -f >/dev/null
sleep 2
docker ps --filter name=photo-sorter --format '{{.Names}}: {{.Status}}'
docker logs photo-sorter 2>&1 | tail -3
"@ -replace "`r", ""
$b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($script))
ssh $Ssh "echo $b64 | base64 -d | bash"
if (-not $?) { throw "deploy failed" }
"Open http://${HostIp}:${Port}/"
