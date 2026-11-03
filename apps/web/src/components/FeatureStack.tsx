import { useEffect, useRef, useState } from "react";
import { gsap } from "gsap";
import { ScrollTrigger } from "gsap/ScrollTrigger";
import * as THREE from "three";

const features = [
  { title: "Send your traffic", body: "Your services send request logs using a project-specific ingest key." },
  { title: "See the shape of traffic", body: "Live request rates, latency and error rates reveal how your API is behaving." },
  { title: "Know when things change", body: "Threshold rules surface anomalies while keeping alert severity deterministic." },
  { title: "Investigate with context", body: "Optional Jev analysis provides advisory context alongside the alert." },
];

gsap.registerPlugin(ScrollTrigger);

export default function FeatureStack() {
  const sectionRef = useRef<HTMLElement>(null);
  const canvasRef = useRef<HTMLDivElement>(null);
  const [active, setActive] = useState(0);

  useEffect(() => {
    const section = sectionRef.current;
    const host = canvasRef.current;
    if (!section || !host || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    let renderer: THREE.WebGLRenderer;
    try { renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true }); } catch { return; }
    renderer.setPixelRatio(Math.min(window.devicePixelRatio, 1.5));
    host.appendChild(renderer.domElement);
    const scene = new THREE.Scene();
    const camera = new THREE.PerspectiveCamera(38, 1, 0.1, 100);
    camera.position.set(5, 4, 7); camera.lookAt(0, 0, 0);
    scene.add(new THREE.HemisphereLight(0xeafff3, 0x132a20, 2));
    const light = new THREE.DirectionalLight(0xffffff, 2); light.position.set(3, 5, 4); scene.add(light);
    const group = new THREE.Group(); scene.add(group);
    const geometry = new THREE.BoxGeometry(2.5, 0.32, 2.5);
    const materials = ["#314a3d", "#3f7957", "#58a776", "#9edeb4"].map((color) => new THREE.MeshStandardMaterial({ color, roughness: 0.65, metalness: 0.15 }));
    const meshes = materials.map((material, index) => { const mesh = new THREE.Mesh(geometry, material); mesh.position.y = index * 0.45 - 0.7 + (index === 0 ? 0 : 1.8); mesh.scale.setScalar(index === 0 ? 1 : 0.85); group.add(mesh); return mesh; });
    const resize = () => { const width = Math.max(host.clientWidth, 1), height = Math.max(host.clientHeight, 1); renderer.setSize(width, height, false); camera.aspect = width / height; camera.updateProjectionMatrix(); renderer.render(scene, camera); };
    const observer = new ResizeObserver(resize); observer.observe(host); resize();
    const trigger = ScrollTrigger.create({ trigger: section, start: "top top", end: "bottom bottom", onUpdate: (self) => {
      const progress = self.progress * (features.length - 1);
      setActive(Math.min(features.length - 1, Math.floor(progress + 0.05)));
      meshes.forEach((mesh, i) => { const phase = Math.max(0, Math.min(1, progress - i + 1)); mesh.position.y = i * 0.45 - 0.7 + (1 - phase) * 1.8; mesh.scale.setScalar(0.85 + 0.15 * phase); });
      group.rotation.y = self.progress * 0.5;
      renderer.render(scene, camera);
    }});
    return () => { trigger.kill(); observer.disconnect(); geometry.dispose(); materials.forEach((m) => m.dispose()); renderer.dispose(); renderer.domElement.remove(); };
  }, []);

  return <section id="features" ref={sectionRef} className="feature-story">
    <div className="feature-sticky"><div className="feature-visual" ref={canvasRef} aria-hidden="true"><div className="feature-fallback">◇</div></div><p className="feature-caption">{features[active].title}</p></div>
    <div className="feature-steps">{features.map((feature, index) => <article className="feature-step" key={feature.title}><span className="feature-index">0{index + 1}</span><h3>{feature.title}</h3><p>{feature.body}</p></article>)}</div>
  </section>;
}
