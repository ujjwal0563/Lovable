package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authmw "lovable-backend/internal/middleware"
)

type TemplatesHandler struct {
	db *pgxpool.Pool
}

func NewTemplatesHandler(db *pgxpool.Pool) *TemplatesHandler {
	return &TemplatesHandler{db: db}
}

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

var builtinTemplates = []Template{
	{ID: "todo-app", Name: "Todo App", Description: "A clean todo app with add, complete, and delete functionality", Category: "productivity"},
	{ID: "dashboard", Name: "Analytics Dashboard", Description: "A beautiful dashboard with charts, stats cards, and sidebar navigation", Category: "business"},
	{ID: "landing-page", Name: "SaaS Landing Page", Description: "A modern landing page with hero, features, pricing, and CTA sections", Category: "marketing"},
	{ID: "blog", Name: "Blog", Description: "A clean blog with post list, post detail, and categories", Category: "content"},
	{ID: "ecommerce", Name: "Product Store", Description: "An e-commerce product listing page with cart and filters", Category: "commerce"},
	{ID: "portfolio", Name: "Portfolio", Description: "A personal portfolio with projects, about, and contact sections", Category: "personal"},
}

// List returns all available templates
// GET /api/templates
func (h *TemplatesHandler) List(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, builtinTemplates, http.StatusOK)
}

// CreateFromTemplate creates a new project seeded with template files
// POST /api/templates/:templateId/use
func (h *TemplatesHandler) CreateFromTemplate(w http.ResponseWriter, r *http.Request) {
	userID := authmw.GetUserID(r)
	templateID := chi.URLParam(r, "templateId")

	var tmpl *Template
	for i := range builtinTemplates {
		if builtinTemplates[i].ID == templateID {
			tmpl = &builtinTemplates[i]
			break
		}
	}
	if tmpl == nil {
		writeError(w, "template not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	projectName := req.Name
	if projectName == "" {
		projectName = tmpl.Name
	}

	// Create project
	var projectID string
	err := h.db.QueryRow(r.Context(),
		`INSERT INTO projects (user_id, name, description)
		 VALUES ($1, $2, $3) RETURNING id`,
		userID, projectName, tmpl.Description,
	).Scan(&projectID)
	if err != nil {
		writeError(w, "failed to create project", http.StatusInternalServerError)
		return
	}

	// Seed template files
	files := getTemplateFiles(templateID)
	for path, content := range files {
		h.db.Exec(r.Context(),
			`INSERT INTO project_files (project_id, path, content)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (project_id, path) DO UPDATE SET content = EXCLUDED.content`,
			projectID, path, content,
		)
	}

	writeJSON(w, map[string]any{
		"id":           projectID,
		"name":         projectName,
		"description":  tmpl.Description,
		"template_id":  templateID,
		"files_seeded": len(files),
	}, http.StatusCreated)
}

// getTemplateFiles returns the starter files for a given template
func getTemplateFiles(templateID string) map[string]string {
	switch templateID {
	case "todo-app":
		return todoAppFiles()
	case "dashboard":
		return dashboardFiles()
	case "landing-page":
		return landingPageFiles()
	case "blog":
		return blogFiles()
	case "ecommerce":
		return ecommerceFiles()
	case "portfolio":
		return portfolioFiles()
	default:
		return map[string]string{}
	}
}

// ── Template file contents ─────────────────────────────────────────────────
// Note: backticks in JSX are escaped as \x60 since Go raw strings use backticks

func todoAppFiles() map[string]string {
	content := strings.Join([]string{
		`import { useState } from 'react'`,
		`import { Plus, Trash2, Check } from 'lucide-react'`,
		``,
		`interface Todo { id: number; text: string; done: boolean }`,
		``,
		`export default function App() {`,
		`  const [todos, setTodos] = useState<Todo[]>([`,
		`    { id: 1, text: 'Build something awesome', done: false },`,
		`    { id: 2, text: 'Learn React', done: true },`,
		`  ])`,
		`  const [input, setInput] = useState('')`,
		``,
		`  const add = () => {`,
		`    if (!input.trim()) return`,
		`    setTodos(prev => [...prev, { id: Date.now(), text: input.trim(), done: false }])`,
		`    setInput('')`,
		`  }`,
		`  const toggle = (id: number) => setTodos(prev => prev.map(t => t.id === id ? { ...t, done: !t.done } : t))`,
		`  const remove = (id: number) => setTodos(prev => prev.filter(t => t.id !== id))`,
		``,
		`  return (`,
		`    <div className="min-h-screen bg-gray-950 flex items-center justify-center p-4">`,
		`      <div className="w-full max-w-md bg-gray-900 rounded-2xl p-6 shadow-2xl">`,
		`        <h1 className="text-2xl font-bold text-white mb-6">My Todos</h1>`,
		`        <div className="flex gap-2 mb-6">`,
		`          <input value={input} onChange={e => setInput(e.target.value)}`,
		`            onKeyDown={e => e.key === 'Enter' && add()}`,
		`            placeholder="Add a new task..."`,
		`            className="flex-1 bg-gray-800 text-white rounded-xl px-4 py-2.5 text-sm focus:outline-none focus:ring-2 focus:ring-violet-500 placeholder-gray-500"`,
		`          />`,
		`          <button onClick={add} className="w-10 h-10 bg-violet-600 hover:bg-violet-500 text-white rounded-xl flex items-center justify-center">`,
		`            <Plus size={18} />`,
		`          </button>`,
		`        </div>`,
		`        <div className="space-y-2">`,
		`          {todos.map(todo => (`,
		`            <div key={todo.id} className="flex items-center gap-3 bg-gray-800 rounded-xl px-4 py-3 group">`,
		`              <button onClick={() => toggle(todo.id)}`,
		`                className={"w-5 h-5 rounded-full border-2 flex items-center justify-center flex-shrink-0 transition-colors " + (todo.done ? 'bg-violet-600 border-violet-600' : 'border-gray-600 hover:border-violet-500')}>`,
		`                {todo.done && <Check size={11} className="text-white" />}`,
		`              </button>`,
		`              <span className={"flex-1 text-sm " + (todo.done ? 'line-through text-gray-500' : 'text-white')}>`,
		`                {todo.text}`,
		`              </span>`,
		`              <button onClick={() => remove(todo.id)} className="opacity-0 group-hover:opacity-100 text-gray-500 hover:text-red-400 transition-all">`,
		`                <Trash2 size={14} />`,
		`              </button>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`        <p className="text-xs text-gray-600 mt-4 text-center">`,
		`          {todos.filter(t => t.done).length} of {todos.length} completed`,
		`        </p>`,
		`      </div>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}

func dashboardFiles() map[string]string {
	content := strings.Join([]string{
		`import { BarChart3, Users, DollarSign, TrendingUp, Home, Settings, Bell } from 'lucide-react'`,
		``,
		`const stats = [`,
		`  { label: 'Total Revenue', value: '$45,231', change: '+20.1%', icon: DollarSign, color: 'text-green-400' },`,
		`  { label: 'Active Users', value: '2,350', change: '+15.3%', icon: Users, color: 'text-blue-400' },`,
		`  { label: 'New Orders', value: '1,247', change: '+8.7%', icon: BarChart3, color: 'text-violet-400' },`,
		`  { label: 'Growth Rate', value: '12.5%', change: '+2.4%', icon: TrendingUp, color: 'text-orange-400' },`,
		`]`,
		``,
		`const navItems = [`,
		`  { icon: Home, label: 'Overview', active: true },`,
		`  { icon: Users, label: 'Users', active: false },`,
		`  { icon: BarChart3, label: 'Analytics', active: false },`,
		`  { icon: Settings, label: 'Settings', active: false },`,
		`]`,
		``,
		`export default function App() {`,
		`  return (`,
		`    <div className="flex h-screen bg-gray-950 text-white">`,
		`      <aside className="w-56 bg-gray-900 border-r border-gray-800 flex flex-col p-4">`,
		`        <div className="flex items-center gap-2 mb-8 px-2">`,
		`          <div className="w-8 h-8 bg-violet-600 rounded-lg" />`,
		`          <span className="font-bold">Dashboard</span>`,
		`        </div>`,
		`        {navItems.map(item => (`,
		`          <button key={item.label} className={"flex items-center gap-3 px-3 py-2.5 rounded-xl text-sm mb-1 transition-colors " + (item.active ? 'bg-violet-600/20 text-violet-300' : 'text-gray-400 hover:text-white hover:bg-gray-800')}>`,
		`            <item.icon size={16} />`,
		`            {item.label}`,
		`          </button>`,
		`        ))}`,
		`      </aside>`,
		`      <main className="flex-1 overflow-auto p-8">`,
		`        <div className="flex items-center justify-between mb-8">`,
		`          <h1 className="text-2xl font-bold">Overview</h1>`,
		`          <button className="relative text-gray-400 hover:text-white">`,
		`            <Bell size={20} />`,
		`            <span className="absolute -top-1 -right-1 w-4 h-4 bg-red-500 rounded-full text-[10px] flex items-center justify-center">3</span>`,
		`          </button>`,
		`        </div>`,
		`        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4 mb-8">`,
		`          {stats.map(stat => (`,
		`            <div key={stat.label} className="bg-gray-900 rounded-2xl p-5 border border-gray-800">`,
		`              <div className="flex items-center justify-between mb-3">`,
		`                <span className="text-gray-400 text-sm">{stat.label}</span>`,
		`                <stat.icon size={18} className={stat.color} />`,
		`              </div>`,
		`              <div className="text-2xl font-bold mb-1">{stat.value}</div>`,
		`              <div className="text-green-400 text-xs">{stat.change} from last month</div>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`        <div className="bg-gray-900 rounded-2xl p-6 border border-gray-800">`,
		`          <h2 className="font-semibold mb-4">Revenue Overview</h2>`,
		`          <div className="flex items-end gap-2 h-40">`,
		`            {[40,65,45,80,55,70,85,60,75,90,70,95].map((h, i) => (`,
		`              <div key={i} className="flex-1 bg-violet-600/30 hover:bg-violet-600/60 rounded-t-md transition-colors" style={{ height: h + '%' }} />`,
		`            ))}`,
		`          </div>`,
		`          <div className="flex justify-between text-xs text-gray-500 mt-2">`,
		`            {['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'].map(m => (`,
		`              <span key={m}>{m}</span>`,
		`            ))}`,
		`          </div>`,
		`        </div>`,
		`      </main>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}

func landingPageFiles() map[string]string {
	content := strings.Join([]string{
		`import { ArrowRight, Check, Zap, Shield, Globe } from 'lucide-react'`,
		``,
		`const features = [`,
		`  { icon: Zap, title: 'Lightning Fast', desc: 'Built for performance from the ground up.' },`,
		`  { icon: Shield, title: 'Secure by Default', desc: 'Enterprise-grade security out of the box.' },`,
		`  { icon: Globe, title: 'Global Scale', desc: 'Deploy to 200+ regions worldwide instantly.' },`,
		`]`,
		``,
		`const plans = [`,
		`  { name: 'Starter', price: '$0', features: ['5 projects', '10GB storage', 'Community support'], popular: false },`,
		`  { name: 'Pro', price: '$20', features: ['Unlimited projects', '100GB storage', 'Priority support', 'Custom domain'], popular: true },`,
		`  { name: 'Enterprise', price: '$99', features: ['Everything in Pro', 'SLA guarantee', 'Dedicated support', 'SSO & SAML'], popular: false },`,
		`]`,
		``,
		`export default function App() {`,
		`  return (`,
		`    <div className="min-h-screen bg-gray-950 text-white">`,
		`      <nav className="border-b border-gray-800 px-8 py-4 flex items-center justify-between">`,
		`        <span className="font-bold text-lg">MyApp</span>`,
		`        <div className="flex gap-6 text-sm text-gray-400">`,
		`          <a href="#" className="hover:text-white">Features</a>`,
		`          <a href="#" className="hover:text-white">Pricing</a>`,
		`          <a href="#" className="hover:text-white">Docs</a>`,
		`        </div>`,
		`        <button className="bg-white text-black px-4 py-2 rounded-lg text-sm font-semibold hover:bg-gray-100">Get Started</button>`,
		`      </nav>`,
		`      <section className="text-center py-32 px-6">`,
		`        <h1 className="text-6xl font-black mb-6 leading-tight">Build faster.<br />Ship sooner.</h1>`,
		`        <p className="text-gray-400 text-xl mb-10 max-w-xl mx-auto">The platform that helps teams build and ship products at the speed of thought.</p>`,
		`        <button className="bg-violet-600 hover:bg-violet-500 text-white px-8 py-4 rounded-xl font-semibold text-lg flex items-center gap-2 mx-auto transition-colors">`,
		`          Start for free <ArrowRight size={20} />`,
		`        </button>`,
		`      </section>`,
		`      <section className="py-20 px-8 max-w-5xl mx-auto">`,
		`        <div className="grid grid-cols-1 md:grid-cols-3 gap-6">`,
		`          {features.map(f => (`,
		`            <div key={f.title} className="bg-gray-900 rounded-2xl p-6 border border-gray-800">`,
		`              <div className="w-10 h-10 bg-violet-600/20 rounded-xl flex items-center justify-center mb-4">`,
		`                <f.icon size={20} className="text-violet-400" />`,
		`              </div>`,
		`              <h3 className="font-bold mb-2">{f.title}</h3>`,
		`              <p className="text-gray-400 text-sm">{f.desc}</p>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`      </section>`,
		`      <section className="py-20 px-8 max-w-5xl mx-auto">`,
		`        <h2 className="text-4xl font-bold text-center mb-12">Simple pricing</h2>`,
		`        <div className="grid grid-cols-1 md:grid-cols-3 gap-6">`,
		`          {plans.map(plan => (`,
		`            <div key={plan.name} className={"rounded-2xl p-6 border " + (plan.popular ? 'bg-violet-600 border-violet-500' : 'bg-gray-900 border-gray-800')}>`,
		`              {plan.popular && <div className="text-xs font-bold mb-3 bg-white/20 rounded-full px-3 py-1 w-fit">MOST POPULAR</div>}`,
		`              <h3 className="font-bold text-lg mb-1">{plan.name}</h3>`,
		`              <div className="text-3xl font-black mb-6">{plan.price}<span className="text-sm font-normal opacity-70">/mo</span></div>`,
		`              <ul className="space-y-2 mb-6">`,
		`                {plan.features.map(f => (`,
		`                  <li key={f} className="flex items-center gap-2 text-sm"><Check size={14} /> {f}</li>`,
		`                ))}`,
		`              </ul>`,
		`              <button className={"w-full py-2.5 rounded-xl font-semibold text-sm transition-colors " + (plan.popular ? 'bg-white text-violet-700 hover:bg-gray-100' : 'bg-gray-800 text-white hover:bg-gray-700')}>`,
		`                Get started`,
		`              </button>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`      </section>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}

func blogFiles() map[string]string {
	content := strings.Join([]string{
		`import { useState } from 'react'`,
		`import { Clock, ArrowLeft, Tag } from 'lucide-react'`,
		``,
		`const posts = [`,
		`  { id: 1, title: 'Getting Started with React', excerpt: 'Learn the basics of React and build your first component.', date: 'Jun 10, 2025', readTime: '5 min', tag: 'Tutorial' },`,
		`  { id: 2, title: 'Why TypeScript is Worth It', excerpt: 'TypeScript adds static typing to JavaScript and makes large codebases manageable.', date: 'Jun 8, 2025', readTime: '8 min', tag: 'Opinion' },`,
		`  { id: 3, title: 'Tailwind CSS Tips and Tricks', excerpt: 'Level up your Tailwind skills with these powerful utility patterns.', date: 'Jun 5, 2025', readTime: '6 min', tag: 'Tips' },`,
		`]`,
		``,
		`export default function App() {`,
		`  const [selected, setSelected] = useState<number | null>(null)`,
		`  const post = posts.find(p => p.id === selected)`,
		``,
		`  if (post) return (`,
		`    <div className="min-h-screen bg-gray-950 text-white p-8 max-w-2xl mx-auto">`,
		`      <button onClick={() => setSelected(null)} className="flex items-center gap-2 text-gray-400 hover:text-white mb-8 text-sm">`,
		`        <ArrowLeft size={16} /> Back to posts`,
		`      </button>`,
		`      <div className="flex items-center gap-2 mb-4">`,
		`        <span className="bg-violet-600/20 text-violet-300 text-xs px-3 py-1 rounded-full">{post.tag}</span>`,
		`        <span className="text-gray-500 text-xs flex items-center gap-1"><Clock size={11} />{post.readTime} read</span>`,
		`      </div>`,
		`      <h1 className="text-4xl font-bold mb-4">{post.title}</h1>`,
		`      <p className="text-gray-400 text-sm mb-8">{post.date}</p>`,
		`      <p className="text-gray-300 leading-relaxed">{post.excerpt}</p>`,
		`      <p className="text-gray-300 leading-relaxed mt-4">This is where the full post content would appear. Ask the AI to fill in detailed content for any topic.</p>`,
		`    </div>`,
		`  )`,
		``,
		`  return (`,
		`    <div className="min-h-screen bg-gray-950 text-white p-8 max-w-2xl mx-auto">`,
		`      <h1 className="text-4xl font-bold mb-2">Blog</h1>`,
		`      <p className="text-gray-400 mb-10">Thoughts on code, design, and building things.</p>`,
		`      <div className="space-y-6">`,
		`        {posts.map(p => (`,
		`          <article key={p.id} onClick={() => setSelected(p.id)}`,
		`            className="bg-gray-900 rounded-2xl p-6 border border-gray-800 hover:border-gray-600 cursor-pointer transition-colors group">`,
		`            <div className="flex items-center gap-2 mb-3">`,
		`              <Tag size={12} className="text-violet-400" />`,
		`              <span className="text-violet-400 text-xs">{p.tag}</span>`,
		`              <span className="text-gray-600 text-xs ml-auto">{p.readTime} read</span>`,
		`            </div>`,
		`            <h2 className="font-bold text-lg mb-2 group-hover:text-violet-300 transition-colors">{p.title}</h2>`,
		`            <p className="text-gray-400 text-sm leading-relaxed mb-3">{p.excerpt}</p>`,
		`            <p className="text-gray-600 text-xs">{p.date}</p>`,
		`          </article>`,
		`        ))}`,
		`      </div>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}

func ecommerceFiles() map[string]string {
	content := strings.Join([]string{
		`import { useState } from 'react'`,
		`import { ShoppingCart, Star, Filter } from 'lucide-react'`,
		``,
		`const products = [`,
		`  { id: 1, name: 'Wireless Headphones', price: 79.99, rating: 4.5, reviews: 128, category: 'Electronics' },`,
		`  { id: 2, name: 'Running Shoes', price: 120.00, rating: 4.8, reviews: 256, category: 'Sports' },`,
		`  { id: 3, name: 'Coffee Maker', price: 49.99, rating: 4.2, reviews: 89, category: 'Kitchen' },`,
		`  { id: 4, name: 'Desk Lamp', price: 34.99, rating: 4.6, reviews: 67, category: 'Home' },`,
		`  { id: 5, name: 'Backpack', price: 89.99, rating: 4.4, reviews: 203, category: 'Sports' },`,
		`  { id: 6, name: 'Smartwatch', price: 199.99, rating: 4.7, reviews: 312, category: 'Electronics' },`,
		`]`,
		``,
		`export default function App() {`,
		`  const [cart, setCart] = useState<number[]>([])`,
		`  const [filter, setFilter] = useState('All')`,
		`  const categories = ['All', 'Electronics', 'Sports', 'Kitchen', 'Home']`,
		`  const filtered = filter === 'All' ? products : products.filter(p => p.category === filter)`,
		`  const addToCart = (id: number) => setCart(prev => [...prev, id])`,
		``,
		`  return (`,
		`    <div className="min-h-screen bg-gray-950 text-white">`,
		`      <header className="border-b border-gray-800 px-8 py-4 flex items-center justify-between">`,
		`        <span className="font-bold text-lg">Shop</span>`,
		`        <div className="flex items-center gap-2">`,
		`          <ShoppingCart size={20} />`,
		`          {cart.length > 0 && <span className="bg-violet-600 text-white text-xs w-5 h-5 rounded-full flex items-center justify-center">{cart.length}</span>}`,
		`        </div>`,
		`      </header>`,
		`      <div className="max-w-6xl mx-auto px-8 py-8">`,
		`        <div className="flex items-center gap-3 mb-8">`,
		`          <Filter size={16} className="text-gray-400" />`,
		`          {categories.map(c => (`,
		`            <button key={c} onClick={() => setFilter(c)}`,
		`              className={"px-4 py-1.5 rounded-full text-sm transition-colors " + (filter === c ? 'bg-violet-600 text-white' : 'bg-gray-800 text-gray-400 hover:text-white')}>`,
		`              {c}`,
		`            </button>`,
		`          ))}`,
		`        </div>`,
		`        <div className="grid grid-cols-2 md:grid-cols-3 gap-5">`,
		`          {filtered.map(p => (`,
		`            <div key={p.id} className="bg-gray-900 rounded-2xl overflow-hidden border border-gray-800 hover:border-gray-600 transition-colors">`,
		`              <div className="h-40 bg-gradient-to-br from-gray-800 to-gray-700 flex items-center justify-center">`,
		`                <span className="text-4xl">package</span>`,
		`              </div>`,
		`              <div className="p-4">`,
		`                <div className="text-xs text-violet-400 mb-1">{p.category}</div>`,
		`                <h3 className="font-semibold mb-2">{p.name}</h3>`,
		`                <div className="flex items-center gap-1 mb-3">`,
		`                  <Star size={12} className="text-yellow-400 fill-yellow-400" />`,
		`                  <span className="text-xs text-gray-400">{p.rating} ({p.reviews})</span>`,
		`                </div>`,
		`                <div className="flex items-center justify-between">`,
		`                  <span className="font-bold">${"{p.price}"}</span>`,
		`                  <button onClick={() => addToCart(p.id)} className="bg-violet-600 hover:bg-violet-500 text-white px-3 py-1.5 rounded-lg text-xs font-semibold transition-colors">`,
		`                    Add to cart`,
		`                  </button>`,
		`                </div>`,
		`              </div>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`      </div>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}

func portfolioFiles() map[string]string {
	content := strings.Join([]string{
		`import { Github, Twitter, Mail, ExternalLink } from 'lucide-react'`,
		``,
		`const projects = [`,
		`  { name: 'AI Chat App', desc: 'Real-time chat with Claude AI integration', tech: ['React', 'Go', 'PostgreSQL'] },`,
		`  { name: 'E-commerce Platform', desc: 'Full-stack store with payments', tech: ['Next.js', 'Stripe', 'Prisma'] },`,
		`  { name: 'Analytics Dashboard', desc: 'Data visualization for business metrics', tech: ['React', 'D3.js', 'Python'] },`,
		`]`,
		``,
		`const skills = ['React', 'TypeScript', 'Go', 'PostgreSQL', 'Tailwind CSS', 'Docker', 'AWS', 'GraphQL']`,
		``,
		`export default function App() {`,
		`  return (`,
		`    <div className="min-h-screen bg-gray-950 text-white">`,
		`      <nav className="max-w-4xl mx-auto px-8 py-6 flex items-center justify-between">`,
		`        <span className="font-bold">Portfolio</span>`,
		`        <div className="flex items-center gap-4 text-gray-400">`,
		`          <a href="#" className="hover:text-white text-sm">About</a>`,
		`          <a href="#" className="hover:text-white text-sm">Projects</a>`,
		`          <a href="#" className="hover:text-white text-sm">Contact</a>`,
		`        </div>`,
		`      </nav>`,
		`      <section className="max-w-4xl mx-auto px-8 py-20">`,
		`        <div className="w-16 h-16 bg-gradient-to-br from-violet-500 to-pink-500 rounded-2xl mb-8" />`,
		`        <p className="text-violet-400 font-medium mb-3">Hi, I am</p>`,
		`        <h1 className="text-6xl font-black mb-4">John Doe</h1>`,
		`        <p className="text-gray-400 text-xl mb-8 max-w-lg">Full-stack developer who loves building beautiful, performant web applications.</p>`,
		`        <div className="flex items-center gap-4">`,
		`          <button className="bg-violet-600 hover:bg-violet-500 text-white px-6 py-3 rounded-xl font-semibold transition-colors flex items-center gap-2">`,
		`            <Mail size={16} /> Get in touch`,
		`          </button>`,
		`          <div className="flex items-center gap-3 text-gray-400">`,
		`            <a href="#" className="hover:text-white"><Github size={20} /></a>`,
		`            <a href="#" className="hover:text-white"><Twitter size={20} /></a>`,
		`          </div>`,
		`        </div>`,
		`      </section>`,
		`      <section className="max-w-4xl mx-auto px-8 py-12 border-t border-gray-800">`,
		`        <h2 className="text-2xl font-bold mb-6">Skills</h2>`,
		`        <div className="flex flex-wrap gap-2">`,
		`          {skills.map(s => <span key={s} className="bg-gray-800 text-gray-300 px-4 py-2 rounded-full text-sm">{s}</span>)}`,
		`        </div>`,
		`      </section>`,
		`      <section className="max-w-4xl mx-auto px-8 py-12 border-t border-gray-800">`,
		`        <h2 className="text-2xl font-bold mb-6">Projects</h2>`,
		`        <div className="grid grid-cols-1 md:grid-cols-3 gap-5">`,
		`          {projects.map(p => (`,
		`            <div key={p.name} className="bg-gray-900 rounded-2xl p-5 border border-gray-800 hover:border-gray-600 transition-colors group">`,
		`              <div className="flex items-start justify-between mb-3">`,
		`                <h3 className="font-bold">{p.name}</h3>`,
		`                <ExternalLink size={14} className="text-gray-500 group-hover:text-violet-400 transition-colors" />`,
		`              </div>`,
		`              <p className="text-gray-400 text-sm mb-4">{p.desc}</p>`,
		`              <div className="flex flex-wrap gap-1">`,
		`                {p.tech.map(t => <span key={t} className="text-xs text-violet-400 bg-violet-600/10 px-2 py-0.5 rounded">{t}</span>)}`,
		`              </div>`,
		`            </div>`,
		`          ))}`,
		`        </div>`,
		`      </section>`,
		`    </div>`,
		`  )`,
		`}`,
	}, "\n")
	return map[string]string{"src/App.tsx": content}
}
