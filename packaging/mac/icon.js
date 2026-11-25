// Draws icon.png (1024x1024) in the current directory.
// Run with the system image tool: sips -j icon.js
var size = 1024;
var canvas = new Canvas(size, size);
var c = canvas;

// Apple icon grid: an 824 px tile centred in 1024 px, so it lines up in Finder.
var inset = 100;
var side = size - inset * 2;
var radius = 185;

function tile(x, y, w, h, r) {
  c.beginPath();
  c.moveTo(x + r, y);
  c.lineTo(x + w - r, y);
  c.quadraticCurveTo(x + w, y, x + w, y + r);
  c.lineTo(x + w, y + h - r);
  c.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  c.lineTo(x + r, y + h);
  c.quadraticCurveTo(x, y + h, x, y + h - r);
  c.lineTo(x, y + r);
  c.quadraticCurveTo(x, y, x + r, y);
  c.closePath();
}

tile(inset, inset, side, side, radius);
c.fillStyle = "#17201f";
c.fill();

// Two ends and the bridge between them.
c.strokeStyle = "#f3efe6";
c.lineWidth = 62;
c.lineCap = "round";
c.beginPath();
c.moveTo(318, 570);
c.bezierCurveTo(390, 300, 634, 300, 706, 570);
c.stroke();

function dot(x, y, r, color) {
  c.beginPath();
  c.arc(x, y, r, 0, Math.PI * 2, false);
  c.closePath();
  c.fillStyle = color;
  c.fill();
}

dot(318, 590, 96, "#f3efe6");
dot(706, 590, 96, "#e0764c");

var output = new Output(canvas, "icon.png");
output.addToQueue();
