FROM node:22-bookworm-slim@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436

EXPOSE 3000
HEALTHCHECK --interval=1s --timeout=1s --retries=30 CMD node -e 'fetch("http://127.0.0.1:3000/").then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))'
CMD ["node", "-e", "require('node:http').createServer((_request,response)=>{response.writeHead(200,{'content-type':'text/plain'});response.end('OPL Workspace READY\\n')}).listen(3000,'0.0.0.0')"]
