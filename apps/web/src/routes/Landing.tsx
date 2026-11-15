import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowUpRight, Github } from "lucide-react";
import { lazy, Suspense } from "react";
import { Link } from "react-router-dom";
import { getOptionalMe } from "../features/identity/api";
import "../styles/landing.css";

const FeatureStack = lazy(() => import("../components/FeatureStack"));

function DashboardPreview() {
  return <div className="landing-dashboard" aria-label="Illustration of the Metric dashboard">
    <div className="landing-dashboard-bar"><span>◉ &nbsp; Metric / Checkout API</span><span>● Live</span></div>
    <div className="landing-dashboard-content"><div className="landing-dashboard-title">Checkout API <span>API key</span></div>
      <div className="landing-dashboard-stats"><div>Request rate<strong>28.4 <small>/s</small></strong></div><div>P95 latency<strong>48 <small>ms</small></strong></div><div>Error rate<strong>0.2<small>%</small></strong></div></div>
      <div className="landing-dashboard-chart"><span>Live traffic</span><svg viewBox="0 0 500 120" preserveAspectRatio="none" aria-hidden="true"><path d="M0 94 L45 86 90 90 135 65 180 73 225 43 270 57 315 35 360 52 405 24 450 38 500 18" fill="none" stroke="#71d59b" strokeWidth="2"/><path d="M0 110 L45 107 90 105 135 104 180 108 225 100 270 103 315 97 360 100 405 97 450 98 500 93" fill="none" stroke="#d8ba76" strokeWidth="1.5"/></svg></div>
      <p>Illustrative data</p>
    </div></div>;
}

export default function Landing() {
  const identity = useQuery({ queryKey: ["entry-identity"], queryFn: getOptionalMe, retry: false });
  return <div className="landing">
    <header className="landing-nav"><Link to="/home" className="landing-brand"><Activity aria-hidden="true" size={19} /> Metric</Link><nav aria-label="Homepage navigation"><a href="#features">Features</a><a href="#architecture">How it works</a><a href="https://github.com/Parth06102006/Metric" target="_blank" rel="noreferrer">GitHub</a>{identity.data ? <Link className="landing-nav-cta" to="/dashboard">Dashboard</Link> : <a className="landing-nav-cta" href="/oauth2/start?rd=%2Fdashboard">Sign in</a>}</nav></header>
    <main>
      <section className="landing-hero"><img src="/art/forest-sketch.svg" alt="" className="landing-forest" /><div className="landing-hero-inner"><div className="landing-hero-copy"><p className="landing-kicker">A clearer view of your APIs</p><h1>See the traffic.<br /><em>Understand the signal.</em></h1><p>Metric brings your API traffic, live trends and meaningful alerts together in one calm place.</p><div className="landing-hero-actions">{identity.data ? <Link className="landing-primary" to="/dashboard">Open dashboard <ArrowUpRight size={17} /></Link> : <a className="landing-primary" href="/oauth2/start?rd=%2Fdashboard">Get started <ArrowUpRight size={17} /></a>}<a className="landing-secondary" href="#architecture">See how it works</a></div></div><DashboardPreview /></div><div className="landing-hero-bottom"><span>Self-hosted observability</span><span>Scroll to explore ↓</span></div></section>
      <div className="landing-intro"><p>From a request to a useful signal.</p><h2>Everything you need to understand what’s happening, layered together.</h2></div>
      <Suspense fallback={<section className="landing-loading">Loading features…</section>}><FeatureStack /></Suspense>
      <section id="architecture" className="landing-architecture"><div><p className="landing-kicker">How it works</p><h2>From request to insight.</h2><p>Your services send request logs to Metric. The API stores snapshots, applies alert rules, and streams updates to your dashboard.</p><p className="landing-note">Optional AI analysis adds context; it never changes alert severity.</p></div><div className="landing-diagram" aria-label="Architecture: services send logs to Metric, which streams metrics and alerts to the dashboard"><div>Services <span>Request logs</span></div><div className="landing-arrow">→</div><div>Metric API <span>Ingest · metrics · rules</span></div><div className="landing-arrow">→</div><div>Dashboard <span>Live view · alerts</span></div></div></section>
    </main>
    <footer className="landing-footer"><div><span>Self-hosted API observability</span><a href="https://github.com/Parth06102006/Metric" target="_blank" rel="noreferrer"><Github size={16} /> GitHub <ArrowUpRight size={14} /></a></div><strong>METRIC</strong><p>Clarity for the traffic you depend on.</p></footer>
  </div>;
}
