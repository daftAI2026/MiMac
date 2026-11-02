import AppKit
import WebKit

final class Delegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
    var window: NSWindow!
    let text = NSTextView(frame: NSRect(x: 20, y: 280, width: 620, height: 70))
    let field = NSTextField(frame: NSRect(x: 20, y: 230, width: 300, height: 30))
    let other = NSTextField(frame: NSRect(x: 340, y: 230, width: 300, height: 30))
    let secure = NSSecureTextField(frame: NSRect(x: 20, y: 185, width: 300, height: 30))
    var web: WKWebView!
    var timer: Timer?
    var sequence = 0
    var busy = false
    var webReady = false
    var directory: URL!
    func applicationDidFinishLaunching(_ note: Notification) {
        guard CommandLine.arguments.count == 2 else { NSApp.terminate(nil); return }
        directory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
        window = NSWindow(contentRect: NSRect(x: 160, y: 180, width: 660, height: 380), styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "MiMac 输入兼容性测试"
        text.isRichText = false; text.font = .systemFont(ofSize: 16)
        text.setAccessibilityIdentifier("matrix.textview")
        field.setAccessibilityIdentifier("matrix.field")
        other.setAccessibilityIdentifier("matrix.other")
        secure.setAccessibilityIdentifier("matrix.secure")
        for v in [text as NSView, field, other, secure] { window.contentView!.addSubview(v) }
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        web = WKWebView(frame: NSRect(x: 20, y: 15, width: 620, height: 155), configuration: config)
        web.navigationDelegate = self
        window.contentView!.addSubview(web)
        web.loadHTMLString("""
        <!doctype html><meta charset="utf-8"><style>input,textarea,[contenteditable]{font:16px system-ui;margin:4px;width:240px} [contenteditable]{border:1px solid #777;display:inline-block;min-height:24px} #rich{width:420px} #rich p{margin:2px 0}</style>
        <input id="input" aria-label="matrix input"><textarea id="textarea" aria-label="matrix textarea"></textarea><div id="editable" contenteditable="true" role="textbox" aria-label="matrix editable"></div><div id="rich" contenteditable="true" role="textbox" aria-multiline="true" aria-label="matrix rich editable"><p>L|<strong>ol</strong><em>d</em>|R</p><p>keep</p></div><input id="password" type="password" aria-label="matrix password">
        """, baseURL: nil)
        window.makeKeyAndOrderFront(nil); window.makeFirstResponder(text)
        NSApp.activate(ignoringOtherApps: true)
        timer = Timer.scheduledTimer(withTimeInterval: 0.03, repeats: true) { [weak self] _ in self?.poll() }
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        webReady = true
        try? String(ProcessInfo.processInfo.processIdentifier).write(to: directory.appendingPathComponent("pid"), atomically: true, encoding: .utf8)
    }
    func poll() {
        guard webReady, !busy,
              let data = try? Data(contentsOf: directory.appendingPathComponent("command.json")),
              let c = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              let seq = c["seq"] as? Int, seq > sequence,
              let action = c["action"] as? String else { return }
        sequence = seq; busy = true
        let target = c["target"] as? String ?? "textview"
        if action == "quit" { NSApp.terminate(nil); return }
        if action == "snapshot" { respond(seq); return }
        if action == "reset" {
            text.string = "L|old|R"; field.stringValue = "L|old|R"; other.stringValue = "untouched"
            secure.stringValue = "" // no password input or value recording
            web.evaluateJavaScript("for (const id of ['input','textarea','editable']) { const e=document.getElementById(id); if(id==='editable')e.textContent='L|old|R';else e.value='L|old|R'; } document.getElementById('rich').innerHTML='<p>L|<strong>ol</strong><em>d</em>|R</p><p>keep</p>'; document.getElementById('password').value='';") { [weak self] _, error in
                if let error { self?.respond(seq, error: String(describing:error)); return }
                self?.focus(target, seq: seq, reset: true)
            }
            return
        }
        if action == "focus" { focus(target, seq: seq, reset: false); return }
        if action == "selection" {
            if target == "textview" { text.setSelectedRange(NSRange(location: 0, length: 0)) }
            else if target == "field" { (window.firstResponder as? NSTextView)?.setSelectedRange(NSRange(location:0,length:0)) }
            else if ["input", "textarea", "editable", "rich"].contains(target) {
                let js = """
                (()=>{let e=document.getElementById('\(target)');let r=document.createRange();if('\(target)'==='editable'){r.setStart(e.firstChild,0);}else if('\(target)'==='rich'){r.setStart(e.querySelector('strong').firstChild,0);}else{e.setSelectionRange(0,0);return true;}r.collapse(true);let s=window.getSelection();s.removeAllRanges();s.addRange(r);return true;})()
                """
                web.evaluateJavaScript(js) { [weak self] _, error in
                    DispatchQueue.main.asyncAfter(deadline:.now()+0.05) { self?.respond(seq,error:error.map{String(describing:$0)}) }
                }
                return
            }
            respond(seq); return
        }
        respond(seq, error:"unknown action")
    }
    func focus(_ target: String, seq: Int, reset: Bool) {
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        switch target {
        case "textview": window.makeFirstResponder(text); if reset { text.setSelectedRange(NSRange(location:2,length:3)) }
        case "field": window.makeFirstResponder(field); if reset { (window.firstResponder as? NSTextView)?.setSelectedRange(NSRange(location:2,length:3)) }
        case "other": window.makeFirstResponder(other)
        case "secure": window.makeFirstResponder(secure)
        case "input", "textarea", "editable", "rich", "password":
            window.makeFirstResponder(web)
            let js = """
            (()=>{let e=document.getElementById('\(target)');e.focus();let r=document.createRange();if('\(target)'==='editable'){r.setStart(e.firstChild,2);r.setEnd(e.firstChild,5);let s=window.getSelection();s.removeAllRanges();s.addRange(r);}else if('\(target)'==='rich'){let a=e.querySelector('strong').firstChild;let b=e.querySelector('em').firstChild;r.setStart(a,0);r.setEnd(b,b.length);let s=window.getSelection();s.removeAllRanges();s.addRange(r);}else if('\(target)'!=='password'){e.setSelectionRange(2,5);}return true;})()
            """
            web.evaluateJavaScript(js) { [weak self] _, error in
                DispatchQueue.main.asyncAfter(deadline:.now()+0.15) { self?.respond(seq,error:error.map{String(describing:$0)}) }
            }
            return
        default: respond(seq,error:"unknown target"); return
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+0.05) { [weak self] in self?.respond(seq) }
    }
    func respond(_ seq: Int, error: String? = nil) {
        // Only fixture-owned synthetic nonsecure content is inspected here.
        web.evaluateJavaScript("({input:document.getElementById('input').value,textarea:document.getElementById('textarea').value,editable:document.getElementById('editable').textContent,rich:document.getElementById('rich').innerText,focused:document.activeElement.id})") { [weak self] value, jsError in
            guard let self else { return }
            var state: [String: Any] = ["seq":seq,"pid":ProcessInfo.processInfo.processIdentifier,"textview":self.text.string,"field":self.field.currentEditor()?.string ?? self.field.stringValue,"other":self.other.stringValue,"web":value as? [String:Any] ?? [:]]
            if let e = error ?? jsError.map({String(describing:$0)}) { state["error"] = e }
            if let data = try? JSONSerialization.data(withJSONObject:state,options:[.sortedKeys]), (try? data.write(to:self.directory.appendingPathComponent("result.json"),options:.atomic)) != nil { }
            self.busy = false
        }
    }
}
let app=NSApplication.shared
let delegate=Delegate()
app.delegate=delegate
app.setActivationPolicy(.regular)
app.run()
