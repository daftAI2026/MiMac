#!/usr/bin/env swift
import AppKit
import ApplicationServices
import CoreGraphics
import CryptoKit
import Darwin
import Foundation
import ImageIO

// Development-only capture of the actual mapping and tray menus. No menu
// command, plugin, permission, input source or remote control is selected.
struct CaptureFailure: Error, CustomStringConvertible {
    let description: String
    init(_ description: String) { self.description = description }
}
let pages = [
    ("device", "设备", "cmd/mimac/settings.go"),
    ("mapping", "按键映射", "cmd/mimac/settings_mapping.go"),
    ("voice", "语音工具", "cmd/mimac/settings_voice.go"),
    ("plugins", "插件", "cmd/mimac/settings_mapping.go"),
    ("general", "通用", "cmd/mimac/native_preferences.go"),
    ("about", "关于", "cmd/mimac/settings_about.go")
]
func attribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
    var value: CFTypeRef?
    return AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success ? value : nil
}
func string(_ element: AXUIElement, _ name: String) -> String? { attribute(element, name) as? String }
func children(_ element: AXUIElement) -> [AXUIElement] { attribute(element, "AXChildren") as? [AXUIElement] ?? [] }
func matches(_ root: AXUIElement, role: String, text: String) throws -> [AXUIElement] {
    var queue = [(root, 0)], cursor = 0, result: [AXUIElement] = []
    while cursor < queue.count {
        guard cursor < 3000 else { throw CaptureFailure("MiMac accessibility tree exceeded the capture bound") }
        let (element, depth) = queue[cursor]; cursor += 1
        if string(element, "AXRole") == role && [string(element, "AXTitle"), string(element, "AXValue"), string(element, "AXDescription")].contains(text) { result.append(element) }
        if depth < 20 && string(element, "AXRole") != "AXMenuBar" {
            queue.append(contentsOf: children(element).map { ($0, depth + 1) })
        }
    }
    return result
}
func wait(_ timeout: TimeInterval, _ predicate: () throws -> Bool) throws {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if try predicate() { return }
        _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.1))
    }
    throw CaptureFailure("Timed out waiting for the requested MiMac UI state")
}
func selectPage(_ label: String, in window: AXUIElement) throws {
    let buttons = try matches(window, role: "AXButton", text: label)
    guard buttons.count == 1, let button = buttons.first else { throw CaptureFailure("Expected one sidebar button: \(label)") }
    guard AXUIElementPerformAction(button, "AXPress" as CFString) == .success else { throw CaptureFailure("Sidebar action failed: \(label)") }
    try wait(8) { try matches(window, role: "AXHeading", text: label).count == 1 }
}
func sha(_ data: Data) -> String { SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined() }
func processStart(_ pid: pid_t) throws -> Date {
    // NSRunningApplication.launchDate is nil for some directly launched App
    // executables. The kernel's start time still binds this PID to the binary.
    var info = proc_bsdinfo()
    let size = Int32(MemoryLayout<proc_bsdinfo>.size)
    guard proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size) == size,
          info.pbi_pid == UInt32(pid), info.pbi_start_tvsec > 0 else {
        throw CaptureFailure("Could not verify the running process start time")
    }
    return Date(timeIntervalSince1970: Double(info.pbi_start_tvsec) + Double(info.pbi_start_tvusec) / 1_000_000)
}
func pixels(_ url: URL) throws -> (String, Int, Int) {
    guard let source = CGImageSourceCreateWithURL(url as CFURL, nil), let image = CGImageSourceCreateImageAtIndex(source, 0, nil),
          let raw = image.dataProvider?.data else { throw CaptureFailure("Window capture is not a decodable image") }
    return (sha(raw as Data), image.width, image.height)
}
func screenshot(_ windowID: UInt32, to url: URL) throws {
    let process = Process(); process.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
    process.arguments = ["-x", "-o", "-l", String(windowID), url.path]
    process.standardOutput = FileHandle.nullDevice
    let errors = Pipe(); process.standardError = errors
    try process.run(); process.waitUntilExit()
    guard process.terminationStatus == 0 else { throw CaptureFailure("Window-only capture failed (exit \(process.terminationStatus)); check Screen Recording permission") }
}
func writeJSON(_ object: Any, to url: URL) throws {
    let data = try JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])
    try (data + Data([10])).write(to: url, options: .atomic)
}

func allElements(_ roots: [AXUIElement]) throws -> [AXUIElement] {
 var queue=roots.map{($0,0)},result:[AXUIElement]=[],cursor=0,seen=Set<CFHashCode>()
 while cursor<queue.count {
  guard cursor<6000 else{throw CaptureFailure("AX tree bound exceeded")}
  let(e,d)=queue[cursor];cursor+=1
  guard seen.insert(CFHash(e)).inserted else{continue};result.append(e)
  if d<25{queue.append(contentsOf:children(e).map{($0,d+1)})}
 }
 return result
}
func label(_ e:AXUIElement)->String { string(e,"AXTitle") ?? string(e,"AXDescription") ?? string(e,"AXValue") ?? "" }
func ownWindows(_ pid:pid_t)->[[String:Any]] {
 (CGWindowListCopyWindowInfo([.optionOnScreenOnly,.excludeDesktopElements],kCGNullWindowID)as?[[String:Any]] ?? []).filter{($0[kCGWindowOwnerPID as String]as?Int32)==pid}
}
func extraRoots(_ app:AXUIElement)->[AXUIElement] {
 var roots=[app]
 if let value=attribute(app,"AXExtrasMenuBar") {
  if CFGetTypeID(value)==AXUIElementGetTypeID(){roots.append(value as! AXUIElement)}
  else if let values=value as?[AXUIElement]{roots+=values}
 }
 if let value=attribute(app,"AXFocusedUIElement"),CFGetTypeID(value)==AXUIElementGetTypeID(){roots.append(value as! AXUIElement)}
 return roots
}
func actions(_ e:AXUIElement)->[String]{var v:CFArray?;guard AXUIElementCopyActionNames(e,&v) == .success else{return []};return v as?[String] ?? []}
func configIdentity()->[String:String]{
 let root=FileManager.default.urls(for:.applicationSupportDirectory,in:.userDomainMask)[0].appendingPathComponent("MiMac")
 var out=[String:String]();for name in ["tools.json","mappings.json","preferences.json"]{if let d=try?Data(contentsOf:root.appendingPathComponent(name)){out[name]=sha(d)}};return out
}
do {
 let args=Array(CommandLine.arguments.dropFirst())
 if args == ["--help"]{print("Usage: script/capture_workbench_states.swift --bundle /absolute/MiMac.app --expected-sha HEX [--inspect]\nOnly opens and cancels the real tray and mapping menus. Requires existing AX and screen access. No recording or configuration selection.");exit(0)}
 guard args.count==4 || (args.count==5 && args[4]=="--inspect"),args[0]=="--bundle",args[2]=="--expected-sha",args[1].hasPrefix("/"),args[3].count==64 else{throw CaptureFailure("Invalid capture arguments")}
 let bundleURL=URL(fileURLWithPath:args[1]).standardizedFileURL.resolvingSymlinksInPath()
 guard let bundle=Bundle(url:bundleURL),bundle.bundleIdentifier=="dev.mimac.app",let exe=bundle.executableURL,sha(try Data(contentsOf:exe))==args[3]else{throw CaptureFailure("Exact bundle identity mismatch")}
 _=NSApplication.shared
 let apps=NSWorkspace.shared.runningApplications.filter{$0.bundleIdentifier=="dev.mimac.app"}
 guard apps.count==1,let app=apps.first,app.bundleURL?.resolvingSymlinksInPath()==bundleURL,app.executableURL?.resolvingSymlinksInPath()==exe.resolvingSymlinksInPath()else{throw CaptureFailure("Expected one exact running MiMac")}
 let start=try processStart(app.processIdentifier),pid=app.processIdentifier
 guard let modified=try FileManager.default.attributesOfItem(atPath:exe.path)[.modificationDate]as?Date,modified<=start else{throw CaptureFailure("Executable changed after process launch")}
 guard AXIsProcessTrusted(),CGPreflightScreenCaptureAccess()else{throw CaptureFailure("Existing AX or Screen Recording access missing; no prompt opened")}
 let root=AXUIElementCreateApplication(pid);guard AXUIElementSetMessagingTimeout(root,1) == .success else{throw CaptureFailure("AX timeout setup failed")}
 let windows=(attribute(root,"AXWindows")as?[AXUIElement] ?? []).filter{string($0,"AXTitle")=="MiMac"}
 guard windows.count==1,let window=windows.first,(attribute(window,"AXSheets")as?[AXUIElement] ?? []).isEmpty else{throw CaptureFailure("Expected one settings window without sheet")}
 if args.count==5 {
  let es=try allElements(extraRoots(root));let records=es.map{["role":string($0,"AXRole") ?? "","label":label($0),"actions":actions($0)] as [String:Any]};let d=try JSONSerialization.data(withJSONObject:["pid":pid,"elements":records,"windows":ownWindows(pid)],options:[.prettyPrinted,.sortedKeys]);print(String(decoding:d,as:UTF8.self));exit(0)
 }
 let original=try pages.filter{try matches(window,role:"AXHeading",text:$0.1).count==1};guard original.count==1 else{throw CaptureFailure("Cannot identify original page")}
 let nativeRoot=bundleURL.appendingPathComponent("Contents/Resources")
 let nativeNames=["libMiMacATVV.dylib","libMiMacProcessTap.dylib","libMiMacReadyIndicator.dylib","libMiMacSettings.dylib","libMiMacSpeech.dylib","libSiriGlassBridge.dylib"]
 var nativeSHA=[String:String]();for name in nativeNames{nativeSHA[name]=sha(try Data(contentsOf:nativeRoot.appendingPathComponent(name)))}
 let scriptURL=URL(fileURLWithPath:CommandLine.arguments[0]).standardizedFileURL
 let captureToolSHA=sha(try Data(contentsOf:scriptURL))
 let configBefore=configIdentity(),previousFront=NSWorkspace.shared.frontmostApplication
 guard configBefore.count==3 else{throw CaptureFailure("Expected complete existing configuration identity")}

 var restored=false,menuToClose:AXUIElement?,openedWindowIDs=Set<UInt32>()
 func remainingMenuWindows()->Set<UInt32>{Set(ownWindows(pid).compactMap{$0[kCGWindowNumber as String]as?UInt32}).intersection(openedWindowIDs)}
 func closeMenu()throws{
  if remainingMenuWindows().isEmpty{menuToClose=nil;openedWindowIDs.removeAll();return}
  if let menu=menuToClose,actions(menu).contains("AXCancel"){
   let result=AXUIElementPerformAction(menu,"AXCancel" as CFString)
   guard result == .success || remainingMenuWindows().isEmpty else{throw CaptureFailure("Owned-menu AXCancel refused: \(result.rawValue)")}
  }else if !remainingMenuWindows().isEmpty {
   // Cancel only a newly observed window belonging to this exact App PID.
   // Posting to the PID cannot send Escape to the user's other foreground app.
   guard let down=CGEvent(keyboardEventSource:nil,virtualKey:53,keyDown:true),let up=CGEvent(keyboardEventSource:nil,virtualKey:53,keyDown:false)else{throw CaptureFailure("Could not construct owned-menu cancel")}
   down.postToPid(pid);up.postToPid(pid)
  }
  try wait(3){remainingMenuWindows().isEmpty}
  menuToClose=nil;openedWindowIDs.removeAll()
 }
 defer {
  var issues=[String]()
  do{try closeMenu()}catch{issues.append("menu: \(error)")}
  if !restored{do{try selectPage(original[0].1,in:window)}catch{issues.append("page: \(error)")}}
  if let previousFront,previousFront.processIdentifier != pid,NSWorkspace.shared.frontmostApplication?.processIdentifier==pid {
   if !previousFront.activate(options:[]){issues.append("foreground activation refused")}
  }
  if !issues.isEmpty{fputs("Capture cleanup incomplete: \(issues.joined(separator:"; "))\n",stderr)}
 }
 _=app.activate(options:[]);try wait(5){NSWorkspace.shared.frontmostApplication?.processIdentifier==pid}
 guard AXUIElementPerformAction(window,"AXRaise" as CFString) == .success,AXUIElementSetAttributeValue(window,"AXMain" as CFString,kCFBooleanTrue) == .success else{throw CaptureFailure("Could not raise exact settings window")}
 let cache=FileManager.default.urls(for:.cachesDirectory,in:.userDomainMask)[0].appendingPathComponent("MiMac/workbench-states")
 try FileManager.default.createDirectory(at:cache,withIntermediateDirectories:true,attributes:[.posixPermissions:0o700]);let run=cache.appendingPathComponent(UUID().uuidString)
 try FileManager.default.createDirectory(at:run,withIntermediateDirectories:false,attributes:[.posixPermissions:0o700]);var records=[[String:Any]]()
 for scene in ["mapping-menu","tray-menu"] {
  guard NSWorkspace.shared.frontmostApplication?.processIdentifier==pid else{throw CaptureFailure("Foreground changed before menu capture")}
  if scene=="mapping-menu"{try selectPage("按键映射",in:window)}
  let es=try allElements(extraRoots(root))
  let candidateElements: [AXUIElement]
  if scene=="tray-menu" {
   guard let bar=attribute(root,"AXExtrasMenuBar"),CFGetTypeID(bar)==AXUIElementGetTypeID()else{throw CaptureFailure("Exact app has no AXExtrasMenuBar")};candidateElements=children(bar as! AXUIElement)
  }else{candidateElements=es}
  let candidates=candidateElements.filter {e in
   let role=string(e,"AXRole") ?? ""
   return scene=="mapping-menu" ? label(e)=="上的预设" && ["AXMenuButton","AXPopUpButton","AXButton"].contains(role) : role=="AXMenuBarItem"
  }
  guard candidates.count==1,let button=candidates.first,actions(button).contains("AXPress") else{throw CaptureFailure("Expected one actionable menu opener: \(scene); found \(candidates.count)")}
  let beforeMenuIDs=Set(es.filter{string($0,"AXRole")=="AXMenu"}.map{CFHash($0)})
  let beforeWindows=ownWindows(pid)
  let beforeIDs=Set(beforeWindows.compactMap{$0[kCGWindowNumber as String]as?UInt32})
  let result=AXUIElementPerformAction(button,"AXPress" as CFString)
  let expected=scene=="mapping-menu" ? ["使用设备默认","录入快捷键…"] : ["按键映射…","设置…","退出 MiMac"]
  var visibleMenu:AXUIElement?,menuWindows=[[String:Any]]()
  try wait(4){
   guard NSWorkspace.shared.frontmostApplication?.processIdentifier==pid else{throw CaptureFailure("Foreground changed while opening menu")}
   let all=try allElements(extraRoots(root));let menus=all.filter{string($0,"AXRole")=="AXMenu"}
   visibleMenu=menus.first{m in let texts=children(m).map{label($0)};return expected.allSatisfy{texts.contains($0)}}
   menuWindows=ownWindows(pid).filter{w in guard let id=w[kCGWindowNumber as String]as?UInt32,let b=w[kCGWindowBounds as String]as?[String:Any],let width=b["Width"]as?Double,let height=b["Height"]as?Double else{return false};return !beforeIDs.contains(id) && width>80 && height>60}
   openedWindowIDs.formUnion(menuWindows.compactMap{$0[kCGWindowNumber as String]as?UInt32})
   if let visibleMenu{menuToClose=visibleMenu}
   else if !openedWindowIDs.isEmpty{menuToClose=menus.first{!beforeMenuIDs.contains(CFHash($0)) && actions($0).contains("AXCancel")}}
   return visibleMenu != nil && menuWindows.count==1
  }
  guard let menu=visibleMenu,let cg=menuWindows.first,let wid=cg[kCGWindowNumber as String]as?UInt32 else{throw CaptureFailure("Could not match actual owned menu window")};menuToClose=menu
  var previous="",stable=false,size=(0,0);let file=run.appendingPathComponent(scene+".png"),deadline=Date().addingTimeInterval(4)
  while Date()<deadline {
   _=RunLoop.current.run(mode:.default,before:Date(timeIntervalSinceNow:0.15))
   guard NSWorkspace.shared.frontmostApplication?.processIdentifier==pid,ownWindows(pid).contains(where:{($0[kCGWindowNumber as String]as?UInt32)==wid})else{throw CaptureFailure("Foreground or owned menu changed during capture")}
   try screenshot(wid,to:file)
   guard NSWorkspace.shared.frontmostApplication?.processIdentifier==pid,ownWindows(pid).contains(where:{($0[kCGWindowNumber as String]as?UInt32)==wid})else{throw CaptureFailure("Foreground or owned menu changed while reading pixels")}
   let(d,w,h)=try pixels(file);size=(w,h);if d==previous{stable=true;break};previous=d
  }
  guard stable else{throw CaptureFailure("Menu pixels did not settle")}
  let cgBounds=cg[kCGWindowBounds as String]as?[String:Any] ?? [:]
  func rect(_ bounds:[String:Any])throws->CGRect{guard let x=bounds["X"]as?Double,let y=bounds["Y"]as?Double,let w=bounds["Width"]as?Double,let h=bounds["Height"]as?Double,w>0,h>0 else{throw CaptureFailure("Invalid owned-window geometry")};return CGRect(x:x,y:y,width:w,height:h)}
  var extent=try rect(cgBounds),capturedIDs=[wid]
  if scene=="mapping-menu" {
   let parents=beforeWindows.filter{($0[kCGWindowName as String]as?String)=="MiMac" && ($0[kCGWindowLayer as String]as?Int)==0}
   guard parents.count==1,let parent=parents.first,let b=parent[kCGWindowBounds as String]as?[String:Any],let parentID=parent[kCGWindowNumber as String]as?UInt32 else{throw CaptureFailure("Could not identify owned menu parent")}
   extent=extent.union(try rect(b));capturedIDs.append(parentID)
  }
  let pixelScale=Double(size.0)/extent.width
  guard pixelScale>=1,pixelScale<=4,abs(Double(size.1)-extent.height*pixelScale)<1 else{throw CaptureFailure("Image does not match owned menu/parent capture extent")}
  records.append(["id":scene,"title":scene=="mapping-menu" ? "按键预设":"菜单栏","state":"菜单展开","file":scene+".png","width":size.0,"height":size.1,"capturedAt":ISO8601DateFormatter().string(from:Date()),"sha256":sha(try Data(contentsOf:file)),"captureExtent":["width":extent.width,"height":extent.height,"pixelScale":pixelScale],"capturedWindowIDs":capturedIDs,"windowID":wid,"window":cgBounds,"openerActionResult":result.rawValue,"menuItems":children(menu).map{label($0)},"sourcePaths":[scene=="mapping-menu" ? "cmd/mimac/settings_mapping.go":"cmd/mimac/tray.go"]])
  try closeMenu();try wait(3){!ownWindows(pid).contains{($0[kCGWindowNumber as String]as?UInt32)==wid}}
 }
 guard NSWorkspace.shared.frontmostApplication?.processIdentifier==pid else{throw CaptureFailure("Foreground changed after menu capture")}
 try selectPage(original[0].1,in:window);restored=true
 for name in nativeNames{guard sha(try Data(contentsOf:nativeRoot.appendingPathComponent(name)))==nativeSHA[name]else{throw CaptureFailure("Native library changed during capture")}}
 guard sha(try Data(contentsOf:scriptURL))==captureToolSHA,configIdentity()==configBefore,try processStart(pid)==start,sha(try Data(contentsOf:exe))==args[3]else{throw CaptureFailure("Configuration or process identity changed")}
 if let previousFront,previousFront.processIdentifier != pid{guard previousFront.activate(options:[])else{throw CaptureFailure("Could not restore previous foreground")};try wait(5){NSWorkspace.shared.frontmostApplication?.processIdentifier==previousFront.processIdentifier}}
 let manifest:[String:Any]=["schema":1,"capturedAt":ISO8601DateFormatter().string(from:Date()),"app":["bundleIdentifier":"dev.mimac.app","executableSHA256":args[3],"nativeLibrarySHA256":nativeSHA,"pid":pid,"processStartedAt":ISO8601DateFormatter().string(from:start),"binaryIdentity":"bundle-on-disk","version":bundle.object(forInfoDictionaryKey:"CFBundleShortVersionString")as?String ?? ""],"originalPageRestored":restored,"foregroundApplicationRestored":true,"configurationUnchanged":true,"menusDismissed":true,"captureToolSHA256":captureToolSHA,"configurationSHA256":configBefore,"pages":records]
 try writeJSON(manifest,to:run.appendingPathComponent("manifest.json"));try writeJSON(["directory":run.lastPathComponent],to:cache.appendingPathComponent("latest.json"));print("Complete state capture: \(run.path)")
}catch{fputs("State capture stopped: \(error)\n",stderr);exit(1)}
