FROM alpine@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

EXPOSE 3000
HEALTHCHECK --interval=1s --timeout=1s --retries=30 CMD wget -q -O- http://127.0.0.1:3000/ || exit 1
CMD ["sh", "-c", "while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 20\r\nConnection: close\r\n\r\nOPL Workspace READY\n'; } | nc -l -p 3000; done"]
