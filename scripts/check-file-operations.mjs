// Disposable Alpine container only; no existing files or DBX settings are changed.
import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { Sidecar } from './sidecar-client.mjs';
const name='dbx-files-'+randomUUID().slice(0,8), dataDir=await mkdtemp(join(tmpdir(),'dbx-files-check-'));
const sidecar=new Sidecar(resolve(process.argv[2]||'dist/verify-sidecar.exe'),dataDir);
const connection={id:name,plugin_id:'io.dbx.docker',host:'127.0.0.1',port:2375,external_config:{protocol:'http',api_version:'auto'}};
const rpc=(method,args={})=>sidecar.request(method,{connectionId:name,...args});
let containerId, buffer=Buffer.alloc(0), timer;
let transferResolve,transferReject; const transfer=new Promise((r,j)=>{transferResolve=r;transferReject=j}); transfer.catch(()=>{});
let chunks=[];
sidecar.child.stdout.on('data',chunk=>{
 buffer=Buffer.concat([buffer,chunk]);
 while(buffer.length>=5){const n=buffer.readUInt32BE(1);if(buffer.length<n+5)return;const kind=buffer[0],body=buffer.subarray(5,n+5);buffer=buffer.subarray(n+5);if(kind!==1)continue;
 const channelLength=body.readUInt16BE(0),channel=body.subarray(2,2+channelLength).toString(),data=body.subarray(2+channelLength);if(channel!=='docker-file')continue;
 const headerLength=data.readUInt32BE(1),header=JSON.parse(data.subarray(5,5+headerLength).toString());
 if(data[0]===1)chunks.push(data.subarray(5+headerLength));
 if(header.status==='error'){clearTimeout(timer);transferReject(Error(header.error))}
 if(header.status==='done'){clearTimeout(timer);transferResolve(Buffer.concat(chunks))}
 }
});
try {
 await rpc('connection/connect',{connection});
 const png='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZgAAAABJRU5ErkJggg==';
 const created=await rpc('docker/createContainer',{request:{name,image:'alpine:latest',command:['/bin/sh','-c',`printf '%s' '${png}' | base64 -d > /tmp/sample.png; printf 'server.port=8080\nserver.name=test\n' > /tmp/application.properties; sleep 300`],start:true}});containerId=created.id;
 const rootEntries=await rpc('docker/listContainerFiles',{containerId,path:'/'});assert(rootEntries.some(entry=>entry.path==='/tmp'));assert(rootEntries.every(entry=>!entry.path.startsWith('//')));
 const op=(operation,path,newName)=>rpc('docker/fileOperation',{containerId,operation,path,name:newName});
 await op('mkdir','/tmp/folder with space');
 await op('create',"/tmp/folder with space/quote'$(touch injected).txt");
 await assert.rejects(op('create',"/tmp/folder with space/quote'$(touch injected).txt"),/exists/);
 await op('rename',"/tmp/folder with space/quote'$(touch injected).txt",'renamed.txt');
 let entries=await rpc('docker/listContainerFiles',{containerId,path:'/tmp/folder with space'});assert.deepEqual(entries.map(e=>e.name),['renamed.txt']);
 assert.equal((await rpc('docker/previewContainerFile',{containerId,path:entries[0].path})).content,'');
 const picture=await rpc('docker/previewContainerFile',{containerId,path:'/tmp/sample.png'});assert.equal(picture.mimeType,'image/png');assert.equal(picture.dataBase64,png);
 const config=await rpc('docker/previewContainerFile',{containerId,path:'/tmp/application.properties'});assert.equal(config.binary,false);assert.match(config.content,/server.port=8080/);
 const sessionId=randomUUID();timer=setTimeout(()=>transferReject(Error('Archive timeout')),15000);
 await rpc('docker/startFileDownload',{containerId,path:'/tmp/folder with space',sessionId,archive:true});
 const archive=await transfer;assert(archive.length>=1024);assert(archive.includes(Buffer.from('renamed.txt')));
 await op('delete','/tmp/folder with space');
 entries=await rpc('docker/listContainerFiles',{containerId,path:'/tmp'});assert(!entries.some(e=>e.name==='folder with space'||e.name==='injected'));
 await assert.rejects(op('delete','/tmp/..'),/non-root/);
 console.log('PASS: create folder/file, reject overwrite, rename, PNG/config preview, tar download, recursive delete, quoted paths and root protection; fixture '+name);
} finally {
 clearTimeout(timer);
 if(containerId){await rpc('docker/containerAction',{containerId,action:'stop'});await rpc('docker/removeContainer',{containerId,force:false});}
 await sidecar.close();
 if(!resolve(dataDir).startsWith(resolve(tmpdir())+sep+'dbx-files-check-'))throw Error('Unexpected temp path');
 await rm(dataDir,{recursive:true,force:true});
}
