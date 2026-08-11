// SPDX-License-Identifier: GPL-3.0-only
import CoreGraphics
import Foundation

guard CommandLine.arguments.count == 2 else {
  FileHandle.standardError.write(Data("usage: render-input-icon.swift OUTPUT.pdf\n".utf8))
  exit(64)
}

let output = URL(fileURLWithPath: CommandLine.arguments[1])
var mediaBox = CGRect(x: 0, y: 0, width: 64, height: 64)
guard let consumer = CGDataConsumer(url: output as CFURL),
      let context = CGContext(consumer: consumer, mediaBox: &mediaBox, nil) else {
  FileHandle.standardError.write(Data("cannot create PDF output\n".utf8))
  exit(74)
}

context.beginPDFPage(nil)
context.setFillColor(CGColor(gray: 0.12, alpha: 1))
context.addPath(CGPath(roundedRect: CGRect(x: 6, y: 13, width: 52, height: 36),
                       cornerWidth: 14, cornerHeight: 14,
                       transform: nil))
context.fillPath()
context.setStrokeColor(CGColor(gray: 1, alpha: 1))
context.setLineWidth(4)
context.setLineCap(.round)
context.move(to: CGPoint(x: 17, y: 31))
context.addLine(to: CGPoint(x: 47, y: 31))
context.move(to: CGPoint(x: 24, y: 40))
context.addLine(to: CGPoint(x: 40, y: 40))
context.strokePath()
context.endPDFPage()
context.closePDF()
