import type { Component } from "solid-js";
import { Chart, Title, Tooltip, Legend, Colors, LineController, CategoryScale, LinearScale, PointElement, LineElement, Filler } from 'chart.js';
import { Line } from 'solid-chartjs';
import type { TrendPoint } from "../services/api";
import { formatNs } from "../utils/format";

Chart.register(Title, Tooltip, Legend, Colors, LineController, CategoryScale, LinearScale, PointElement, LineElement, Filler);

interface Props {
    data: TrendPoint[]; 
    range?: number;
}

const TrendChart: Component<Props> = (props) => {
    const chartData = () => {
        const limit = props.range || 100;
        const showData = props.data.slice(0, limit).reverse();
        
        return {
            labels: showData.map(d => new Date(d.run_date).toLocaleDateString()),
            datasets: [
                {
                    label: 'Min',
                    data: showData.map(d => d.min_ns),
                    borderColor: 'transparent',
                    pointRadius: 0,
                    pointHoverRadius: 0,
                    fill: false,
                },
                {
                    label: 'Max',
                    data: showData.map(d => d.max_ns),
                    borderColor: 'transparent',
                    backgroundColor: 'rgba(9, 105, 218, 0.15)',
                    pointRadius: 0,
                    pointHoverRadius: 0,
                    fill: '-1',
                },
                {
                    label: 'Average',
                    data: showData.map(d => d.avg_ns),
                    borderColor: '#0969da',
                    backgroundColor: 'transparent',
                    borderWidth: 2,
                    tension: 0,
                    pointRadius: 2,
                    fill: false,
                }
            ]
        };
    };

    const chartOptions = {
        responsive: true,
        maintainAspectRatio: false,
        animation: false,
        interaction: {
            mode: 'index',
            intersect: false,
        },
        plugins: { 
            legend: { display: false },
            tooltip: {
                callbacks: {
                    label: function(context: any) {
                        let label = context.dataset.label || '';
                        if (label) {
                            label += ': ';
                        }
                        if (context.parsed.y !== null) {
                            label += formatNs(context.parsed.y);
                        }
                        return label;
                    }
                }
            }
        },
        scales: {
            y: { 
                beginAtZero: true, 
                grid: { color: '#f3f4f6' },
                ticks: {
                    callback: function(value: any) {
                        return formatNs(value);
                    }
                }
            },
            x: { display: false }
        }
    };

    return (
        <div class="relative w-full h-full">
            <Line data={chartData()} options={chartOptions} width={500} height={300} />
        </div>
    );
};

export default TrendChart;
