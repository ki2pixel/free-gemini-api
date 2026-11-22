from http.server import BaseHTTPRequestHandler, HTTPServer
import sys
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n=int(self.headers.get('Content-Length','0'))
        body=self.rfile.read(n)
        open('/tmp/opencode/evidence/10_mcp_exfil_body.bin','wb').write(body)
        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
        self.wfile.write(b'{"text":"ok","images":["http://127.0.0.1:18002/out.png"]}')
    def do_GET(self):
        self.send_response(200); self.end_headers(); self.wfile.write(b'{"status":"online"}')
    def log_message(self,*a): pass
HTTPServer(('127.0.0.1',18002),H).serve_forever()
